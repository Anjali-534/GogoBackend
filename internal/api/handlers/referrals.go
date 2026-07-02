package handlers

import (
    "context"
    "crypto/rand"
    "fmt"
    "log"
    "math/big"
    "net/http"
    "strings"
    "time"

    "github.com/deploykit/backend/internal/db"
    "github.com/gin-gonic/gin"
    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/xuri/excelize/v2"
)

const (
    referralLevel1Amount = 50.00
    referralLevel2Amount = 25.00
)

// MigrateReferrals creates/upgrades the referral schema and backfills
// codes for accounts that predate this feature. Idempotent — safe to run
// on every server start (mirrors the MigrateNotifications pattern).
func MigrateReferrals() error {
    ctx := context.Background()
    pool := db.GetDB().GetPool()

    steps := []string{
        `ALTER TABLE riders  ADD COLUMN IF NOT EXISTS referral_code TEXT UNIQUE`,
        `ALTER TABLE riders  ADD COLUMN IF NOT EXISTS referred_by_code TEXT`,
        `ALTER TABLE riders  ADD COLUMN IF NOT EXISTS referred_by_id UUID`,
        `ALTER TABLE riders  ADD COLUMN IF NOT EXISTS wallet_balance DECIMAL(10,2) DEFAULT 0`,
        `ALTER TABLE riders  ADD COLUMN IF NOT EXISTS first_ride_completed BOOLEAN DEFAULT FALSE`,
        `ALTER TABLE drivers ADD COLUMN IF NOT EXISTS referral_code TEXT UNIQUE`,
        `ALTER TABLE drivers ADD COLUMN IF NOT EXISTS referred_by_code TEXT`,
        `ALTER TABLE drivers ADD COLUMN IF NOT EXISTS referred_by_id UUID`,
        `ALTER TABLE drivers ADD COLUMN IF NOT EXISTS first_trip_completed BOOLEAN DEFAULT FALSE`,
        `CREATE TABLE IF NOT EXISTS referral_rewards (
            id             UUID PRIMARY KEY,
            user_type      TEXT NOT NULL,
            beneficiary_id UUID NOT NULL,
            new_user_id    UUID NOT NULL,
            level          INT NOT NULL,
            amount         DECIMAL(10,2) NOT NULL,
            status         TEXT NOT NULL DEFAULT 'pending',
            credited_at    TIMESTAMPTZ,
            created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
        )`,
        `CREATE INDEX IF NOT EXISTS idx_ref_rewards_benef ON referral_rewards(beneficiary_id)`,
        `CREATE INDEX IF NOT EXISTS idx_ref_rewards_new    ON referral_rewards(new_user_id)`,
    }
    for _, sql := range steps {
        if _, err := pool.Exec(ctx, sql); err != nil {
            log.Printf("MigrateReferrals step failed: %v\nSQL: %s", err, sql)
            return err
        }
    }

    backfillReferralCodes(ctx, "riders", "GU")
    backfillReferralCodes(ctx, "drivers", "GD")
    return nil
}

func backfillReferralCodes(ctx context.Context, table, prefix string) {
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE referral_code IS NULL`, table))
    if err != nil {
        return
    }
    var ids []string
    for rows.Next() {
        var id string
        if rows.Scan(&id) == nil {
            ids = append(ids, id)
        }
    }
    rows.Close()
    for _, id := range ids {
        code := generateReferralCode(ctx, table, prefix)
        pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET referral_code=$1 WHERE id=$2`, table), code, id)
    }
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I — avoids ambiguity when read aloud

func randomCodeSuffix(n int) string {
    b := make([]byte, n)
    for i := range b {
        idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
        b[i] = codeAlphabet[idx.Int64()]
    }
    return string(b)
}

// generateReferralCode returns a unique PREFIX+6char code, retrying on
// collision (astronomically unlikely at this scale, but cheap to guard).
func generateReferralCode(ctx context.Context, table, prefix string) string {
    pool := db.GetDB().GetPool()
    for i := 0; i < 10; i++ {
        code := prefix + randomCodeSuffix(6)
        var exists bool
        pool.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE referral_code=$1)`, table), code).Scan(&exists)
        if !exists {
            return code
        }
    }
    return prefix + randomCodeSuffix(10)
}

// applyReferral links a new signup to their referrer's code and queues
// pending referral_rewards for level 1 (direct, ₹50) and level 2
// (grand-referrer, ₹25). Invalid/unknown codes are ignored silently —
// a referral code must never block signup.
func applyReferral(userType string, newUserID uuid.UUID, referredByCode string) {
    referredByCode = strings.ToUpper(strings.TrimSpace(referredByCode))
    if referredByCode == "" {
        return
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    table := "riders"
    if userType == "driver" {
        table = "drivers"
    }

    var level1ID string
    var level1ReferredBy *string
    err := pool.QueryRow(ctx,
        fmt.Sprintf(`SELECT id, referred_by_id::text FROM %s WHERE referral_code=$1`, table),
        referredByCode,
    ).Scan(&level1ID, &level1ReferredBy)
    if err != nil || level1ID == "" || level1ID == newUserID.String() {
        return // unknown code, or (impossible at signup, but guard anyway) self-referral
    }

    pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET referred_by_code=$1, referred_by_id=$2 WHERE id=$3`, table),
        referredByCode, level1ID, newUserID)

    pool.Exec(ctx, `
        INSERT INTO referral_rewards (id, user_type, beneficiary_id, new_user_id, level, amount, status)
        VALUES ($1, $2, $3, $4, 1, $5, 'pending')
    `, uuid.New(), userType, level1ID, newUserID, referralLevel1Amount)

    if level1ReferredBy != nil && *level1ReferredBy != "" {
        pool.Exec(ctx, `
            INSERT INTO referral_rewards (id, user_type, beneficiary_id, new_user_id, level, amount, status)
            VALUES ($1, $2, $3, $4, 2, $5, 'pending')
        `, uuid.New(), userType, *level1ReferredBy, newUserID, referralLevel2Amount)
    }
}

// creditReferralRewards fires the first time a rider completes their
// first ride, or a driver completes their first trip. The atomic
// UPDATE...WHERE flag=false guard prevents double-crediting if this is
// ever called twice for the same completion.
func creditReferralRewards(userType, userID string) {
    if userID == "" {
        return
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()

    table, flagCol := "riders", "first_ride_completed"
    if userType == "driver" {
        table, flagCol = "drivers", "first_trip_completed"
    }

    var justCompleted string
    err := pool.QueryRow(ctx,
        fmt.Sprintf(`UPDATE %s SET %s = true WHERE id = $1 AND %s = false RETURNING id`, table, flagCol, flagCol),
        userID,
    ).Scan(&justCompleted)
    if err != nil {
        return // not the first completion — nothing to credit
    }

    rows, err := pool.Query(ctx, `
        SELECT id, beneficiary_id, level, amount FROM referral_rewards
        WHERE new_user_id = $1 AND user_type = $2 AND status = 'pending'
    `, userID, userType)
    if err != nil {
        return
    }
    type pendingReward struct {
        id, beneficiaryID string
        level             int
        amount            float64
    }
    var pending []pendingReward
    for rows.Next() {
        var r pendingReward
        if rows.Scan(&r.id, &r.beneficiaryID, &r.level, &r.amount) == nil {
            pending = append(pending, r)
        }
    }
    rows.Close()

    for _, r := range pending {
        pool.Exec(ctx, `UPDATE referral_rewards SET status='credited', credited_at=NOW() WHERE id=$1`, r.id)
        pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET wallet_balance = COALESCE(wallet_balance,0) + $1 WHERE id=$2`, table),
            r.amount, r.beneficiaryID)
        if userType == "driver" {
            // Mirror into driver_earnings so it shows up in the existing ledger UI.
            pool.Exec(ctx, `
                INSERT INTO driver_earnings (id, driver_id, amount, type, description, is_debit, created_at)
                VALUES ($1, $2, $3, 'referral', $4, false, NOW())
            `, uuid.New(), r.beneficiaryID, r.amount, fmt.Sprintf("Referral bonus — level %d", r.level))
        }
    }
}

// resolveReferralActor figures out whether the authenticated user is a
// rider or a driver, and returns their referral identity.
func resolveReferralActor(ctx context.Context, userID string) (userType, actorID, code string, ok bool) {
    pool := db.GetDB().GetPool()
    var id, refCode string
    if err := pool.QueryRow(ctx, `SELECT id, COALESCE(referral_code,'') FROM riders WHERE user_id=$1`, userID).Scan(&id, &refCode); err == nil {
        return "rider", id, refCode, true
    }
    if err := pool.QueryRow(ctx, `SELECT id, COALESCE(referral_code,'') FROM drivers WHERE user_id=$1`, userID).Scan(&id, &refCode); err == nil {
        return "driver", id, refCode, true
    }
    return "", "", "", false
}

func maskName(name string) string {
    name = strings.TrimSpace(name)
    if name == "" {
        return "A gogoo user"
    }
    runes := []rune(name)
    if len(runes) <= 3 {
        return string(runes) + "***"
    }
    return string(runes[:3]) + "***"
}

// GET /gogoo/referral/my-code
func GetMyReferralCode(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    userType, actorID, code, ok := resolveReferralActor(ctx, userID)
    if !ok {
        c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
        return
    }
    pool := db.GetDB().GetPool()

    // Lazily backfill a missing code (predates this feature, or the
    // startup backfill hasn't run yet) so the caller never sees blank.
    if code == "" {
        table := "riders"
        if userType == "driver" {
            table = "drivers"
        }
        prefix := "GU"
        if userType == "driver" {
            prefix = "GD"
        }
        code = generateReferralCode(ctx, table, prefix)
        if _, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET referral_code=$1 WHERE id=$2`, table), code, actorID); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate referral code"})
            return
        }
    }

    var totalReferred int
    var totalEarned, pendingRewards float64
    pool.QueryRow(ctx, `SELECT COUNT(*) FROM referral_rewards WHERE beneficiary_id=$1 AND user_type=$2 AND level=1`, actorID, userType).Scan(&totalReferred)
    pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM referral_rewards WHERE beneficiary_id=$1 AND user_type=$2 AND status='credited'`, actorID, userType).Scan(&totalEarned)
    pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM referral_rewards WHERE beneficiary_id=$1 AND user_type=$2 AND status='pending'`, actorID, userType).Scan(&pendingRewards)

    pathSeg := "r"
    if userType == "driver" {
        pathSeg = "dr"
    }
    c.JSON(http.StatusOK, gin.H{
        "referral_code":   code,
        "share_link":      fmt.Sprintf("%s/%s/%s", backendPublicURL(), pathSeg, code),
        "total_referred":  totalReferred,
        "total_earned":    totalEarned,
        "pending_rewards": pendingRewards,
    })
}

// GET /gogoo/referral/my-referrals
func GetMyReferrals(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    userType, actorID, _, ok := resolveReferralActor(ctx, userID)
    if !ok {
        c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
        return
    }
    table := "riders"
    if userType == "driver" {
        table = "drivers"
    }
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, fmt.Sprintf(`
        SELECT COALESCE(u.name,''), rr.level, rr.amount, rr.status, rr.created_at, rr.credited_at
        FROM referral_rewards rr
        JOIN %s t   ON t.id = rr.new_user_id
        JOIN users u ON u.id = t.user_id
        WHERE rr.beneficiary_id = $1 AND rr.user_type = $2
        ORDER BY rr.created_at DESC
    `, table), actorID, userType)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()

    out := []gin.H{}
    for rows.Next() {
        var name, status string
        var level int
        var amount float64
        var createdAt time.Time
        var creditedAt *time.Time
        if rows.Scan(&name, &level, &amount, &status, &createdAt, &creditedAt) != nil {
            continue
        }
        out = append(out, gin.H{
            "name":        maskName(name),
            "level":       level,
            "amount":      amount,
            "status":      status,
            "joined_date": createdAt,
            "credited_at": creditedAt,
        })
    }
    c.JSON(http.StatusOK, out)
}

// POST /gogoo/referral/validate
func ValidateReferralCode(c *gin.Context) {
    var req struct {
        Code string `json:"code"`
    }
    c.ShouldBindJSON(&req)
    code := strings.ToUpper(strings.TrimSpace(req.Code))
    if code == "" {
        c.JSON(http.StatusOK, gin.H{"valid": false})
        return
    }
    table := "riders"
    if strings.HasPrefix(code, "GD") {
        table = "drivers"
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var name string
    err := pool.QueryRow(ctx,
        fmt.Sprintf(`SELECT u.name FROM %s t JOIN users u ON u.id=t.user_id WHERE t.referral_code=$1`, table),
        code,
    ).Scan(&name)
    if err != nil {
        c.JSON(http.StatusOK, gin.H{"valid": false})
        return
    }
    c.JSON(http.StatusOK, gin.H{"valid": true, "referrer_name": maskName(name)})
}

// referralAdminQuery is shared between the JSON admin endpoint and the xlsx export.
// Level-1 rows are the referral graph's edges (referrer -> referred);
// level-2 rows are just the derived grand-referrer bonus for the same signup,
// so the panel's chain view is built from level-1 rows, with level-2 rows
// looked up separately to annotate the chain bonus.
const referralAdminQuery = `
    SELECT rr.id, rr.user_type, rr.level, rr.amount, rr.status, rr.created_at, rr.credited_at,
           rr.beneficiary_id, rr.new_user_id,
           COALESCE(ub.name,'') AS beneficiary_name, COALESCE(rb.phone, db.phone, '') AS beneficiary_phone,
           COALESCE(rb.referral_code, db.referral_code, '') AS beneficiary_code,
           COALESCE(un.name,'') AS referred_name,     COALESCE(rn.phone, dn.phone, '') AS referred_phone,
           COALESCE(rn.referral_code, dn.referral_code, '') AS referred_code
    FROM referral_rewards rr
    LEFT JOIN riders  rb ON rr.user_type='rider'  AND rb.id = rr.beneficiary_id
    LEFT JOIN drivers db ON rr.user_type='driver' AND db.id = rr.beneficiary_id
    LEFT JOIN riders  rn ON rr.user_type='rider'  AND rn.id = rr.new_user_id
    LEFT JOIN drivers dn ON rr.user_type='driver' AND dn.id = rr.new_user_id
    LEFT JOIN users ub ON ub.id = COALESCE(rb.user_id, db.user_id)
    LEFT JOIN users un ON un.id = COALESCE(rn.user_id, dn.user_id)
    ORDER BY rr.created_at DESC
    LIMIT 1000
`

type referralRow struct {
    ID, UserType, Status                                  string
    Level                                                 int
    Amount                                                float64
    CreatedAt                                             time.Time
    CreditedAt                                            *time.Time
    ReferrerID, ReferrerName, ReferrerPhone, ReferrerCode string
    ReferredID, ReferredName, ReferredPhone, ReferredCode string
}

func scanReferralRows(rows pgx.Rows) []referralRow {
    var out []referralRow
    for rows.Next() {
        var r referralRow
        if rows.Scan(&r.ID, &r.UserType, &r.Level, &r.Amount, &r.Status, &r.CreatedAt, &r.CreditedAt,
            &r.ReferrerID, &r.ReferredID,
            &r.ReferrerName, &r.ReferrerPhone, &r.ReferrerCode,
            &r.ReferredName, &r.ReferredPhone, &r.ReferredCode) != nil {
            continue
        }
        out = append(out, r)
    }
    return out
}

// GET /gogoo/referral/all — full referral tree + summary stats for the master panel.
func AdminListReferrals(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, referralAdminQuery)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    parsed := scanReferralRows(rows)

    out := []gin.H{}
    totalReferrals, riderReferrals, driverReferrals := 0, 0, 0
    var totalPaid, totalPending float64
    for _, r := range parsed {
        out = append(out, gin.H{
            "id": r.ID, "user_type": r.UserType, "level": r.Level, "amount": r.Amount, "status": r.Status,
            "created_at": r.CreatedAt, "credited_at": r.CreditedAt,
            "referrer_id": r.ReferrerID, "referrer_name": r.ReferrerName, "referrer_phone": r.ReferrerPhone, "referrer_code": r.ReferrerCode,
            "referred_id": r.ReferredID, "referred_name": r.ReferredName, "referred_phone": r.ReferredPhone, "referred_code": r.ReferredCode,
        })
        if r.Level == 1 {
            totalReferrals++
            if r.UserType == "rider" {
                riderReferrals++
            } else if r.UserType == "driver" {
                driverReferrals++
            }
        }
        if r.Status == "credited" {
            totalPaid += r.Amount
        } else if r.Status == "pending" {
            totalPending += r.Amount
        }
    }

    c.JSON(http.StatusOK, gin.H{
        "referrals":        out,
        "total_referrals":  totalReferrals,
        "rider_referrals":  riderReferrals,
        "driver_referrals": driverReferrals,
        "total_paid":       totalPaid,
        "total_pending":    totalPending,
    })
}

// GET /gogoo/export/referrals.xlsx
func ExportReferralsXLSX(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, referralAdminQuery)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    parsed := scanReferralRows(rows)

    f := excelize.NewFile()
    sheet := "Referrals"
    f.SetSheetName("Sheet1", sheet)
    headers := []string{
        "Referrer", "Referrer Phone", "Referrer Code", "Referred", "Referred Phone", "Referred Code",
        "Type", "Level", "Amount (₹)", "Status", "Created On", "Credited On",
    }
    hs := headerStyle(f)
    for i, h := range headers {
        cell, _ := excelize.CoordinatesToCellName(i+1, 1)
        f.SetCellValue(sheet, cell, h)
        f.SetCellStyle(sheet, cell, cell, hs)
    }

    rowIdx := 2
    for _, r := range parsed {
        creditedStr := ""
        if r.CreditedAt != nil {
            creditedStr = r.CreditedAt.Format("2006-01-02 15:04")
        }
        vals := []interface{}{
            r.ReferrerName, r.ReferrerPhone, r.ReferrerCode, r.ReferredName, r.ReferredPhone, r.ReferredCode,
            r.UserType, r.Level, r.Amount, r.Status, r.CreatedAt.Format("2006-01-02 15:04"), creditedStr,
        }
        for i, v := range vals {
            cell, _ := excelize.CoordinatesToCellName(i+1, rowIdx)
            f.SetCellValue(sheet, cell, v)
        }
        rowIdx++
    }
    f.SetColWidth(sheet, "A", "F", 22)
    f.SetColWidth(sheet, "G", "L", 16)

    writeXLSX(c, f, fmt.Sprintf("gogoo-referrals-%s.xlsx", time.Now().Format("2006-01-02")))
}
