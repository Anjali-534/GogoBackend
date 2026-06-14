package handlers

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/deploykit/backend/internal/db"
    "github.com/gin-gonic/gin"
    "github.com/google/uuid"
    "golang.org/x/crypto/bcrypt"
)

func RiderSignup(c *gin.Context) {
    var req struct {
        Email    string `json:"email" binding:"required,email"`
        Name     string `json:"name" binding:"required"`
        Password string `json:"password" binding:"required,min=8"`
        Phone    string `json:"phone" binding:"required"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var count int
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM users WHERE email=$1", req.Email).Scan(&count)
    if count > 0 {
        c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
        return
    }
    userID := uuid.New()
    riderID := uuid.New()
    tx, _ := pool.Begin(ctx)
    defer tx.Rollback(ctx)
    hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
    tx.Exec(ctx, "INSERT INTO users (id,email,name,password_hash,is_verified) VALUES ($1,$2,$3,$4,true)", userID, req.Email, req.Name, string(hashedPassword))
    tx.Exec(ctx, "INSERT INTO riders (id,user_id,phone) VALUES ($1,$2,$3)", riderID, userID, req.Phone)
    tx.Commit(ctx)
    c.JSON(http.StatusCreated, gin.H{"user_id": userID, "rider_id": riderID, "message": "Rider account created"})
}

func DriverSignup(c *gin.Context) {
    var req struct {
        Email           string `json:"email" binding:"required,email"`
        Name            string `json:"name" binding:"required"`
        Password        string `json:"password" binding:"required,min=8"`
        Phone           string `json:"phone" binding:"required"`
        LicenseNum      string `json:"license_number"`
        VehicleType     string `json:"vehicle_type"`
        VehicleCategory string `json:"vehicle_category"`
        VehicleNum      string `json:"vehicle_number"`
        VehicleModel    string `json:"vehicle_model"`
        VehicleColor    string `json:"vehicle_color"`
        BankAccountHolder string `json:"bank_account_holder"`
        BankAccountNumber string `json:"bank_account_number"`
        BankIFSC          string `json:"bank_ifsc"`
        BankName          string `json:"bank_name"`
        UPIID             string `json:"upi_id"`
        GSTNumber         string `json:"gst_number"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    if req.VehicleType == "" { req.VehicleType = "cab_4w" }
    if req.VehicleCategory == "" {
        switch {
        case len(req.VehicleType) >= 6 && req.VehicleType[:6] == "truck_":
            req.VehicleCategory = "truck"
        case len(req.VehicleType) >= 9 && req.VehicleType[:9] == "ambulance":
            req.VehicleCategory = "ambulance"
        default:
            req.VehicleCategory = "cab"
        }
    }
    if req.VehicleNum == "" { req.VehicleNum = "PENDING" }
    if req.VehicleModel == "" { req.VehicleModel = "N/A" }
    if req.VehicleColor == "" { req.VehicleColor = "N/A" }
    if req.LicenseNum == "" { req.LicenseNum = "PENDING" }

    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var count int
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM users WHERE email=$1", req.Email).Scan(&count)
    if count > 0 {
        c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
        return
    }
    userID := uuid.New()
    driverID := uuid.New()
    tx, err := pool.Begin(ctx)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
        return
    }
    defer tx.Rollback(ctx)
    hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
    if _, err := tx.Exec(ctx, "INSERT INTO users (id,email,name,password_hash,is_verified) VALUES ($1,$2,$3,$4,false)", userID, req.Email, req.Name, string(hashedPassword)); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
        return
    }
    if _, err := tx.Exec(ctx,
        `INSERT INTO drivers (id,user_id,phone,license_number,vehicle_type,vehicle_category,vehicle_number,vehicle_model,vehicle_color,bank_account_holder,bank_account_number,bank_ifsc,bank_name,upi_id,gst_number) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
        driverID, userID, req.Phone, req.LicenseNum, req.VehicleType, req.VehicleCategory, req.VehicleNum, req.VehicleModel, req.VehicleColor,
        nullIfEmpty(req.BankAccountHolder), nullIfEmpty(req.BankAccountNumber), nullIfEmpty(req.BankIFSC), nullIfEmpty(req.BankName), nullIfEmpty(req.UPIID), nullIfEmpty(req.GSTNumber),
    ); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create driver: " + err.Error()})
        return
    }
    if err := tx.Commit(ctx); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit"})
        return
    }
    c.JSON(http.StatusCreated, gin.H{"user_id": userID, "driver_id": driverID, "message": "Driver account created. Pending verification."})
}

func nullIfEmpty(s string) interface{} {
    if s == "" { return nil }
    return s
}

func ListServiceTypes(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, `SELECT id,name,slug,vehicle_type,COALESCE(category,''),COALESCE(scope,''),base_fare,per_km_rate,per_min_rate,surge_multiplier,capacity FROM service_types WHERE is_active=true ORDER BY base_fare ASC`)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    var services []map[string]interface{}
    for rows.Next() {
        var id, name, slug, vehicleType, category, scope string
        var baseFare, perKm, perMin, surge float64
        var capacity int
        rows.Scan(&id, &name, &slug, &vehicleType, &category, &scope, &baseFare, &perKm, &perMin, &surge, &capacity)
        services = append(services, map[string]interface{}{"id": id, "name": name, "slug": slug, "vehicle_type": vehicleType, "category": category, "scope": scope, "base_fare": baseFare, "per_km_rate": perKm, "surge_multiplier": surge, "capacity": capacity})
    }
    c.JSON(http.StatusOK, services)
}

func CreateBooking(c *gin.Context) {
    var req struct {
        RiderID       string  `json:"rider_id" binding:"required"`
        ServiceTypeID string  `json:"service_type_id" binding:"required"`
        PickupLat     float64 `json:"pickup_lat" binding:"required"`
        PickupLng     float64 `json:"pickup_lng" binding:"required"`
        PickupAddress string  `json:"pickup_address" binding:"required"`
        DropLat       float64 `json:"drop_lat" binding:"required"`
        DropLng       float64 `json:"drop_lng" binding:"required"`
        DropAddress   string  `json:"drop_address" binding:"required"`
        EstimatedFare float64 `json:"estimated_fare"`
        DistanceKm    float64 `json:"distance_km"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    bookingID := uuid.New()
    _, err := pool.Exec(ctx, `INSERT INTO bookings (id,rider_id,service_type_id,status,pickup_lat,pickup_lng,pickup_address,drop_lat,drop_lng,drop_address,estimated_fare,distance_km) VALUES ($1,$2,$3,'searching',$4,$5,$6,$7,$8,$9,$10,$11)`,
        bookingID, req.RiderID, req.ServiceTypeID, req.PickupLat, req.PickupLng, req.PickupAddress, req.DropLat, req.DropLng, req.DropAddress, req.EstimatedFare, req.DistanceKm)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create booking: " + err.Error()})
        return
    }
    c.JSON(http.StatusCreated, gin.H{"booking_id": bookingID, "status": "searching"})
}

func ListBookings(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    status := c.Query("status")
    limit := 50
    query := `SELECT b.id, b.status, b.pickup_address, b.drop_address, b.estimated_fare, b.final_fare, b.created_at, u_r.name as rider_name, COALESCE(u_d.name,'') as driver_name, st.name as service_name FROM bookings b JOIN riders r ON r.id=b.rider_id JOIN users u_r ON u_r.id=r.user_id LEFT JOIN drivers d ON d.id=b.driver_id LEFT JOIN users u_d ON u_d.id=d.user_id JOIN service_types st ON st.id=b.service_type_id`
    args := []interface{}{}
    if status != "" {
        query += " WHERE b.status=$1"
        args = append(args, status)
    }
    query += " ORDER BY b.created_at DESC LIMIT $" + fmt.Sprintf("%d", len(args)+1)
    args = append(args, limit)
    rows, err := pool.Query(ctx, query, args...)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    var bookings []map[string]interface{}
    for rows.Next() {
        var id, status, pickup, drop, riderName, driverName, serviceName string
        var estFare, finalFare *float64
        var createdAt time.Time
        rows.Scan(&id, &status, &pickup, &drop, &estFare, &finalFare, &createdAt, &riderName, &driverName, &serviceName)
        bookings = append(bookings, map[string]interface{}{"id": id, "status": status, "pickup_address": pickup, "drop_address": drop, "estimated_fare": estFare, "final_fare": finalFare, "created_at": createdAt, "rider_name": riderName, "driver_name": driverName, "service_name": serviceName})
    }
    c.JSON(http.StatusOK, bookings)
}

func ListRiderBookings(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, `SELECT b.id, b.status, b.pickup_address, b.drop_address, COALESCE(b.estimated_fare,0), COALESCE(b.final_fare,0), COALESCE(b.distance_km,0), b.created_at, COALESCE(u_d.name,'') as driver_name, st.name as service_name FROM bookings b JOIN riders r ON r.id = b.rider_id JOIN users u_r ON u_r.id = r.user_id LEFT JOIN drivers d ON d.id = b.driver_id LEFT JOIN users u_d ON u_d.id = d.user_id JOIN service_types st ON st.id = b.service_type_id WHERE u_r.id = $1 ORDER BY b.created_at DESC LIMIT 100`, userID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    var bookings []map[string]interface{}
    for rows.Next() {
        var id, status, pickup, drop, driverName, serviceName string
        var estimatedFare, finalFare, distanceKm float64
        var createdAt time.Time
        rows.Scan(&id, &status, &pickup, &drop, &estimatedFare, &finalFare, &distanceKm, &createdAt, &driverName, &serviceName)
        bookings = append(bookings, map[string]interface{}{"id": id, "status": status, "pickup_address": pickup, "drop_address": drop, "estimated_fare": estimatedFare, "final_fare": finalFare, "distance_km": distanceKm, "created_at": createdAt, "driver_name": driverName, "service_name": serviceName})
    }
    if bookings == nil { bookings = []map[string]interface{}{} }
    c.JSON(http.StatusOK, bookings)
}

func ListDrivers(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, `
        SELECT d.id, u.name, u.email, d.phone, d.vehicle_type,
               COALESCE(d.vehicle_category,''), d.vehicle_number, d.vehicle_model,
               d.is_verified, d.is_online, d.is_active,
               d.rating, d.total_rides, d.total_earnings, d.created_at,
               COALESCE(d.is_blocked, FALSE),
               d.blocked_until,
               COALESCE(d.block_reason, '')
        FROM drivers d
        JOIN users u ON u.id = d.user_id
        ORDER BY d.created_at DESC
        LIMIT 100`)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    var drivers []map[string]interface{}
    for rows.Next() {
        var id, name, email, phone, vType, vCategory, vNum, vModel, blockReason string
        var isVerified, isOnline, isActive, isBlocked bool
        var rating, earnings float64
        var totalRides int
        var createdAt time.Time
        var blockedUntil *time.Time
        rows.Scan(&id, &name, &email, &phone, &vType, &vCategory, &vNum, &vModel,
            &isVerified, &isOnline, &isActive, &rating, &totalRides, &earnings, &createdAt,
            &isBlocked, &blockedUntil, &blockReason)
        drivers = append(drivers, map[string]interface{}{
            "id": id, "name": name, "email": email, "phone": phone,
            "vehicle_type": vType, "vehicle_category": vCategory,
            "vehicle_number": vNum, "vehicle_model": vModel,
            "is_verified": isVerified, "is_online": isOnline, "is_active": isActive,
            "rating": rating, "total_rides": totalRides, "total_earnings": earnings,
            "created_at": createdAt,
            "is_blocked": isBlocked, "blocked_until": blockedUntil, "block_reason": blockReason,
        })
    }
    c.JSON(http.StatusOK, drivers)
}

func VerifyDriver(c *gin.Context) {
    driverID := c.Param("id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    _, err := pool.Exec(ctx, "UPDATE drivers SET is_verified=true,updated_at=NOW() WHERE id=$1", driverID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify driver"})
        return
    }
    c.JSON(http.StatusOK, gin.H{"message": "Driver verified"})
}

func GetAnalytics(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var totalBookings, activeDrivers, onlineDrivers, totalRiders int
    var totalRevenue float64
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM bookings").Scan(&totalBookings)
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM drivers WHERE is_verified=true").Scan(&activeDrivers)
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM drivers WHERE is_online=true").Scan(&onlineDrivers)
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM riders").Scan(&totalRiders)
    pool.QueryRow(ctx, "SELECT COALESCE(SUM(amount),0) FROM payments WHERE status='completed'").Scan(&totalRevenue)
    rows, _ := pool.Query(ctx, `SELECT DATE(created_at) as day, COUNT(*) as count FROM bookings WHERE created_at > NOW() - INTERVAL '7 days' GROUP BY day ORDER BY day`)
    defer rows.Close()
    var dailyBookings []map[string]interface{}
    for rows.Next() {
        var day time.Time
        var count int
        rows.Scan(&day, &count)
        dailyBookings = append(dailyBookings, map[string]interface{}{"day": day.Format("Mon"), "count": count})
    }
    c.JSON(http.StatusOK, gin.H{"total_bookings": totalBookings, "active_drivers": activeDrivers, "online_drivers": onlineDrivers, "total_riders": totalRiders, "total_revenue": totalRevenue, "daily_bookings": dailyBookings})
}

func ListPayments(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    rows, err := pool.Query(ctx, `SELECT p.id, p.amount, p.platform_fee, p.driver_earnings, p.method, p.status, p.created_at, u.name as rider_name, b.pickup_address, b.drop_address FROM payments p JOIN riders r ON r.id=p.rider_id JOIN users u ON u.id=r.user_id JOIN bookings b ON b.id=p.booking_id ORDER BY p.created_at DESC LIMIT 100`)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()
    var payments []map[string]interface{}
    for rows.Next() {
        var id, method, status, riderName, pickup, drop string
        var amount, fee, earnings float64
        var createdAt time.Time
        rows.Scan(&id, &amount, &fee, &earnings, &method, &status, &createdAt, &riderName, &pickup, &drop)
        payments = append(payments, map[string]interface{}{"id": id, "amount": amount, "platform_fee": fee, "driver_earnings": earnings, "method": method, "status": status, "created_at": createdAt, "rider_name": riderName, "pickup_address": pickup, "drop_address": drop})
    }
    c.JSON(http.StatusOK, payments)
}

func ToggleDriverOnline(c *gin.Context) {
    driverID := c.Param("id")
    var req struct {
        IsOnline bool    `json:"is_online"`
        Lat      float64 `json:"lat"`
        Lng      float64 `json:"lng"`
    }
    c.ShouldBindJSON(&req)
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    pool.Exec(ctx, "UPDATE drivers SET is_online=$1,current_lat=$2,current_lng=$3,updated_at=NOW() WHERE id=$4", req.IsOnline, req.Lat, req.Lng, driverID)
    c.JSON(http.StatusOK, gin.H{"is_online": req.IsOnline})
}

func AcceptBooking(c *gin.Context) {
    bookingID := c.Param("id")
    var req struct {
        DriverID string `json:"driver_id"`
    }
    c.ShouldBindJSON(&req)
    ctx  := context.Background()
    pool := db.GetDB().GetPool()

    // Reject blocked drivers
    var isBlocked bool
    var blockedUntil *time.Time
    pool.QueryRow(ctx,
        `SELECT COALESCE(is_blocked,FALSE), blocked_until FROM drivers WHERE id=$1`,
        req.DriverID,
    ).Scan(&isBlocked, &blockedUntil)

    if isBlocked && blockedUntil != nil && blockedUntil.After(time.Now()) {
        c.JSON(http.StatusForbidden, gin.H{
            "error":         "You are temporarily blocked due to excessive cancellations",
            "blocked_until": blockedUntil,
        })
        return
    }

    // Auto-lift expired blocks
    if isBlocked && (blockedUntil == nil || !blockedUntil.After(time.Now())) {
        pool.Exec(ctx, `UPDATE drivers SET is_blocked=FALSE, blocked_until=NULL, block_reason=NULL WHERE id=$1`, req.DriverID)
    }

    // Reject if driver already has an active ride
    var activeCount int
    pool.QueryRow(ctx,
        `SELECT COUNT(*) FROM bookings WHERE driver_id=$1 AND status NOT IN ('completed','cancelled')`,
        req.DriverID,
    ).Scan(&activeCount)
    if activeCount > 0 {
        c.JSON(http.StatusConflict, gin.H{"error": "You already have an active ride. Complete it before accepting a new one."})
        return
    }

    pool.Exec(ctx, `UPDATE bookings SET driver_id=$1,status='accepted',accepted_at=NOW() WHERE id=$2 AND status='searching'`, req.DriverID, bookingID)
    c.JSON(http.StatusOK, gin.H{"status": "accepted"})
}

func UpdateBookingStatus(c *gin.Context) {
    bookingID := c.Param("id")
    var req struct {
        Status       string  `json:"status"`
        FinalFare    float64 `json:"final_fare"`
        CancelBy     string  `json:"cancelled_by"`
        CancelReason string  `json:"cancel_reason"`
    }
    c.ShouldBindJSON(&req)
    ctx  := context.Background()
    pool := db.GetDB().GetPool()

    switch req.Status {
    case "arriving":
        pool.Exec(ctx, `UPDATE bookings SET status='arriving',arrived_at=NOW() WHERE id=$1`, bookingID)
    case "in_progress":
        pool.Exec(ctx, `UPDATE bookings SET status='in_progress',started_at=NOW() WHERE id=$1`, bookingID)
    case "completed":
        pool.Exec(ctx, `UPDATE bookings SET status='completed',completed_at=NOW(),final_fare=$1 WHERE id=$2`, req.FinalFare, bookingID)
    case "cancelled":
        pool.Exec(ctx, `UPDATE bookings SET status='cancelled',cancelled_at=NOW(),cancelled_by=$1,cancel_reason=$2 WHERE id=$3`, req.CancelBy, req.CancelReason, bookingID)

        // Auto-block driver if they cancel 2+ rides within 1 hour
        if req.CancelBy == "driver" {
            var driverID string
            err := pool.QueryRow(ctx, `SELECT driver_id FROM bookings WHERE id=$1`, bookingID).Scan(&driverID)
            if err == nil && driverID != "" {
                var cancelCount int
                pool.QueryRow(ctx, `
                    SELECT COUNT(*) FROM bookings
                    WHERE driver_id   = $1
                      AND cancelled_by = 'driver'
                      AND cancelled_at > NOW() - INTERVAL '1 hour'
                `, driverID).Scan(&cancelCount)

                if cancelCount >= 2 {
                    blockedUntil := time.Now().Add(48 * time.Hour)
                    reason := fmt.Sprintf("Auto-blocked: %d cancellations within 1 hour", cancelCount)
                    pool.Exec(ctx, `
                        UPDATE drivers
                        SET is_blocked    = TRUE,
                            blocked_until = $1,
                            block_reason  = $2,
                            updated_at    = NOW()
                        WHERE id = $3
                    `, blockedUntil, reason, driverID)

                    c.JSON(http.StatusOK, gin.H{
                        "status":        "cancelled",
                        "driver_blocked": true,
                        "blocked_until":  blockedUntil,
                        "block_reason":   reason,
                    })
                    return
                }
            }
        }
    }
    c.JSON(http.StatusOK, gin.H{"status": req.Status})
}

func RateBooking(c *gin.Context) {
    bookingID := c.Param("id")
    var req struct {
        RaterType string `json:"rater_type"`
        Rating    int    `json:"rating"`
        Review    string `json:"review"`
    }
    c.ShouldBindJSON(&req)
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    if req.RaterType == "rider" {
        pool.Exec(ctx, "UPDATE bookings SET driver_rating=$1,driver_review=$2 WHERE id=$3", req.Rating, req.Review, bookingID)
        pool.Exec(ctx, `UPDATE drivers SET rating=(SELECT ROUND(AVG(driver_rating)::numeric,2) FROM bookings WHERE driver_id=(SELECT driver_id FROM bookings WHERE id=$1) AND driver_rating IS NOT NULL) WHERE id=(SELECT driver_id FROM bookings WHERE id=$1)`, bookingID)
    } else {
        pool.Exec(ctx, "UPDATE bookings SET rider_rating=$1,rider_review=$2 WHERE id=$3", req.Rating, req.Review, bookingID)
        pool.Exec(ctx, `UPDATE riders SET rating=(SELECT ROUND(AVG(rider_rating)::numeric,2) FROM bookings WHERE rider_id=(SELECT rider_id FROM bookings WHERE id=$1) AND rider_rating IS NOT NULL) WHERE id=(SELECT rider_id FROM bookings WHERE id=$1)`, bookingID)
    }
    c.JSON(http.StatusOK, gin.H{"message": "Rating submitted"})
}

func GetRiderProfile(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var riderID, phone string
    var rating float64
    var totalRides int
    err := pool.QueryRow(ctx, `SELECT r.id, COALESCE(r.phone,''), COALESCE(r.rating,0), COALESCE(r.total_rides,0) FROM riders r WHERE r.user_id=$1`, userID).Scan(&riderID, &phone, &rating, &totalRides)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": "rider profile not found"})
        return
    }
    c.JSON(http.StatusOK, gin.H{"rider_id": riderID, "phone": phone, "rating": rating, "total_rides": totalRides})
}

func GetSavedPlaces(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var savedAddresses []byte
    err := pool.QueryRow(ctx, `SELECT COALESCE(saved_addresses, '[]'::jsonb) FROM riders WHERE user_id=$1`, userID).Scan(&savedAddresses)
    if err != nil {
        c.JSON(http.StatusOK, []interface{}{})
        return
    }
    if len(savedAddresses) == 0 || string(savedAddresses) == "null" {
        c.JSON(http.StatusOK, []interface{}{})
        return
    }
    c.Data(http.StatusOK, "application/json", savedAddresses)
}

func SavePlace(c *gin.Context) {
    userID := c.GetString("user_id")
    var req struct {
        Label   string  `json:"label"`
        Address string  `json:"address"`
        Lat     float64 `json:"lat"`
        Lng     float64 `json:"lng"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    if req.Label == "" || req.Address == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "label and address required"})
        return
    }
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    var existing []byte
    err := pool.QueryRow(ctx, `SELECT COALESCE(saved_addresses, '[]'::jsonb) FROM riders WHERE user_id=$1`, userID).Scan(&existing)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": "rider not found"})
        return
    }
    newEntry := fmt.Sprintf(`{"label":%q,"address":%q,"lat":%f,"lng":%f}`, req.Label, req.Address, req.Lat, req.Lng)
    _, err = pool.Exec(ctx, `UPDATE riders SET saved_addresses=(COALESCE((SELECT jsonb_agg(elem) FROM jsonb_array_elements(COALESCE(saved_addresses,'[]'::jsonb)) elem WHERE elem->>'label' != $2),'[]'::jsonb))||$3::jsonb,updated_at=NOW() WHERE user_id=$1`, userID, req.Label, "["+newEntry+"]")
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save: " + err.Error()})
        return
    }
    c.JSON(http.StatusOK, gin.H{"message": "place saved", "label": req.Label})
}

func DeleteSavedPlace(c *gin.Context) {
    userID := c.GetString("user_id")
    label := c.Param("label")
    ctx := context.Background()
    pool := db.GetDB().GetPool()
    _, err := pool.Exec(ctx, `UPDATE riders SET saved_addresses=(SELECT COALESCE(jsonb_agg(elem),'[]'::jsonb) FROM jsonb_array_elements(COALESCE(saved_addresses,'[]'::jsonb)) elem WHERE elem->>'label' != $2),updated_at=NOW() WHERE user_id=$1`, userID, label)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete place"})
        return
    }
    c.JSON(http.StatusOK, gin.H{"message": "place deleted"})
}

// GET /gogoo/driver/bookings
func ListDriverBookings(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()

    rows, err := pool.Query(ctx, `
        SELECT b.id, b.status, b.pickup_address, b.drop_address,
               COALESCE(b.final_fare, b.estimated_fare, 0),
               COALESCE(b.distance_km, 0),
               b.created_at,
               COALESCE(u_r.name, '') AS rider_name,
               st.name AS service_name
        FROM bookings b
        JOIN drivers d   ON d.id = b.driver_id
        JOIN users u_d   ON u_d.id = d.user_id
        JOIN riders r    ON r.id = b.rider_id
        JOIN users u_r   ON u_r.id = r.user_id
        JOIN service_types st ON st.id = b.service_type_id
        WHERE u_d.id = $1
        ORDER BY b.created_at DESC
        LIMIT 50
    `, userID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()

    var bookings []map[string]interface{}
    for rows.Next() {
        var id, status, pickup, drop, riderName, serviceName string
        var fare, distanceKm float64
        var createdAt time.Time
        rows.Scan(&id, &status, &pickup, &drop, &fare, &distanceKm, &createdAt, &riderName, &serviceName)
        bookings = append(bookings, map[string]interface{}{
            "id":             id,
            "status":         status,
            "pickup_address": pickup,
            "drop_address":   drop,
            "fare":           fare,
            "distance_km":    distanceKm,
            "created_at":     createdAt,
            "rider_name":     riderName,
            "service_name":   serviceName,
        })
    }
    if bookings == nil {
        bookings = []map[string]interface{}{}
    }
    c.JSON(http.StatusOK, bookings)
}

// PATCH /gogoo/drivers/:id/block  (admin — manually block or unblock a driver)
func ManageDriverBlock(c *gin.Context) {
    driverID := c.Param("id")
    var req struct {
        Action      string `json:"action"`       // "block" | "unblock"
        Reason      string `json:"reason"`
        DurationHrs int    `json:"duration_hrs"` // default 48 if omitted
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
        return
    }

    ctx  := context.Background()
    pool := db.GetDB().GetPool()

    switch req.Action {
    case "unblock":
        _, err := pool.Exec(ctx, `
            UPDATE drivers
            SET is_blocked    = FALSE,
                blocked_until = NULL,
                block_reason  = NULL,
                updated_at    = NOW()
            WHERE id = $1
        `, driverID)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"message": "Driver unblocked"})

    case "block":
        hrs := req.DurationHrs
        if hrs <= 0 {
            hrs = 48
        }
        reason := req.Reason
        if reason == "" {
            reason = "Manually blocked by admin"
        }
        blockedUntil := time.Now().Add(time.Duration(hrs) * time.Hour)
        _, err := pool.Exec(ctx, `
            UPDATE drivers
            SET is_blocked    = TRUE,
                blocked_until = $1,
                block_reason  = $2,
                updated_at    = NOW()
            WHERE id = $3
        `, blockedUntil, reason, driverID)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"message": "Driver blocked", "blocked_until": blockedUntil})

    default:
        c.JSON(http.StatusBadRequest, gin.H{"error": "action must be 'block' or 'unblock'"})
    }
}

// GET /gogoo/driver/reviews
func GetDriverReviews(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx    := context.Background()
    pool   := db.GetDB().GetPool()

    rows, err := pool.Query(ctx, `
        SELECT b.driver_rating, COALESCE(b.driver_review,''), b.created_at, u.name as rider_name
        FROM bookings b
        JOIN drivers d ON d.id = b.driver_id
        JOIN users u_d ON u_d.id = d.user_id
        JOIN riders r ON r.id = b.rider_id
        JOIN users u ON u.id = r.user_id
        WHERE u_d.id = $1
        AND b.driver_rating IS NOT NULL
        ORDER BY b.created_at DESC
        LIMIT 10
    `, userID)
    if err != nil {
        c.JSON(http.StatusOK, []interface{}{})
        return
    }
    defer rows.Close()

    var reviews []map[string]interface{}
    for rows.Next() {
        var riderName, driverReview string
        var driverRating int
        var createdAt time.Time
        rows.Scan(&driverRating, &driverReview, &createdAt, &riderName)
        reviews = append(reviews, map[string]interface{}{
            "driver_rating": driverRating,
            "driver_review": driverReview,
            "created_at":    createdAt,
            "rider_name":    riderName,
        })
    }
    if reviews == nil {
        reviews = []map[string]interface{}{}
    }
    c.JSON(http.StatusOK, reviews)
}

// GET /gogoo/drivers/:id/bookings  (admin — fetch any driver's ride history by driver UUID)
func ListDriverBookingsByID(c *gin.Context) {
    driverID     := c.Param("id")
    statusFilter := c.Query("status")
    ctx  := context.Background()
    pool := db.GetDB().GetPool()

    query := `
        SELECT b.id, b.status, b.pickup_address, b.drop_address,
               COALESCE(b.final_fare, b.estimated_fare, 0),
               COALESCE(b.distance_km, 0),
               b.created_at,
               COALESCE(u_r.name, '') AS rider_name,
               st.name               AS service_name
        FROM bookings b
        JOIN riders       r   ON r.id   = b.rider_id
        JOIN users        u_r ON u_r.id = r.user_id
        JOIN service_types st ON st.id  = b.service_type_id
        WHERE b.driver_id = $1`

    args := []interface{}{driverID}
    if statusFilter != "" {
        query += " AND b.status = $2"
        args = append(args, statusFilter)
    }
    query += " ORDER BY b.created_at DESC LIMIT 100"

    rows, err := pool.Query(ctx, query, args...)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
        return
    }
    defer rows.Close()

    var bookings []map[string]interface{}
    for rows.Next() {
        var id, status, pickup, drop, riderName, serviceName string
        var fare, distanceKm float64
        var createdAt time.Time
        rows.Scan(&id, &status, &pickup, &drop, &fare, &distanceKm, &createdAt, &riderName, &serviceName)
        bookings = append(bookings, map[string]interface{}{
            "id":             id,
            "status":         status,
            "pickup_address": pickup,
            "drop_address":   drop,
            "fare":           fare,
            "distance_km":    distanceKm,
            "created_at":     createdAt,
            "rider_name":     riderName,
            "service_name":   serviceName,
        })
    }
    if bookings == nil {
        bookings = []map[string]interface{}{}
    }
    c.JSON(http.StatusOK, bookings)
}
