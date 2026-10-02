// Command restore_driver_docs rebuilds driver_documents rows from the files
// still sitting in Cloudinary under gogoo/drivers/. Migration 006 used to
// DROP and recreate driver_documents on every boot (RunFileMigrations
// re-runs every numbered file), wiping every row on each deploy — but
// UploadDriverDocument never deleted the Cloudinary files, so they survive.
//
// Upload public_ids look like gogoo/drivers/<driver_id>/<doc_type>_<8 hex>,
// so driver and doc_type are recoverable; doc_number, expiry_date and review
// history are not. For each (driver, doc_type) the newest file wins.
//
// Restored rows get status 'approved' (reviewed_at=NOW()) when the driver is
// already is_verified, otherwise 'pending', and review_note =
// 'restored-from-cloudinary' so they stay identifiable. A (driver, doc_type)
// that already has a row — directly or via an alias such as
// national_permit/permit — is left alone.
//
// An approved row is flagged in the dry run when its file was uploaded after
// the driver's docs_verified_at (or docs_verified_at is NULL, i.e. verified
// manually), since that file would be approved without ever being reviewed.
//
// Dry run (default) only prints the plan; -apply writes it in one
// transaction. The review_note column comes from migration 066, shipped with
// the non-destructive 006 — the script refuses to run without it, which also
// guarantees the next boot won't wipe the restored rows again.
//
//	DATABASE_URL=postgres://... CLOUDINARY_CLOUD_NAME=... CLOUDINARY_API_KEY=... CLOUDINARY_API_SECRET=... \
//	  go run ./cmd/restore_driver_docs            # dry run
//	  go run ./cmd/restore_driver_docs -apply
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

const (
	cloudPrefix = "gogoo/drivers/"
	restoreNote = "restored-from-cloudinary"
)

// validDocTypes mirrors driver_documents_doc_type_check (migration 016).
var validDocTypes = map[string]bool{
	"passport_photo": true, "aadhaar": true, "aadhaar_front": true, "aadhaar_back": true,
	"pan_card": true, "driving_license": true, "driving_license_front": true, "driving_license_back": true,
	"rc": true, "rc_front": true, "rc_back": true, "insurance": true, "puc": true, "pollution_cert": true,
	"fitness": true, "fitness_cert": true, "permit": true, "national_permit": true, "gst_cert": true,
	"emt_cert": true, "goods_insurance": true, "bank_passbook": true, "vehicle_photo": true,
	"vehicle_photo_front": true, "vehicle_photo_side": true, "police_clearance": true,
}

// docTypeAliases mirrors handlers.docTypeAliases.
var docTypeAliases = map[string]string{"national_permit": "permit"}

func canonical(docType string) string {
	if c, ok := docTypeAliases[docType]; ok {
		return c
	}
	return docType
}

var publicIDRe = regexp.MustCompile(`^gogoo/drivers/([0-9a-fA-F-]{36})/([a-z_]+)_[0-9a-f]{8}$`)

type resource struct {
	PublicID     string `json:"public_id"`
	Format       string `json:"format"`
	ResourceType string `json:"resource_type"`
	CreatedAt    string `json:"created_at"`
	Bytes        int    `json:"bytes"`
	SecureURL    string `json:"secure_url"`
}

type candidate struct {
	driverID, docType, email string
	verified                 bool
	verifiedAt               *time.Time
	res                      resource
	createdAt                time.Time
}

func main() {
	apply := flag.Bool("apply", false, "write the restored rows (default: dry run, print only)")
	flag.Parse()

	_ = godotenv.Load()
	dbURL := os.Getenv("DATABASE_URL")
	cloud, key, secret := os.Getenv("CLOUDINARY_CLOUD_NAME"), os.Getenv("CLOUDINARY_API_KEY"), os.Getenv("CLOUDINARY_API_SECRET")
	if dbURL == "" || cloud == "" || key == "" || secret == "" {
		log.Fatal("DATABASE_URL, CLOUDINARY_CLOUD_NAME, CLOUDINARY_API_KEY and CLOUDINARY_API_SECRET are required")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer conn.Close(ctx)

	var hasReviewNote bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'driver_documents' AND column_name = 'review_note')`,
	).Scan(&hasReviewNote); err != nil {
		log.Fatalf("check review_note column: %v", err)
	}
	if !hasReviewNote {
		log.Fatal("driver_documents.review_note does not exist — deploy the backend with migration 066 " +
			"(and the non-destructive 006) first, otherwise the next boot would wipe the restored rows")
	}

	// Drivers: id -> email, is_verified, docs_verified_at.
	type driverInfo struct {
		email      string
		verified   bool
		verifiedAt *time.Time
	}
	drivers := map[string]driverInfo{}
	rows, err := conn.Query(ctx, `
		SELECT d.id::text, COALESCE(u.email, ''), COALESCE(d.is_verified, false), d.docs_verified_at
		FROM drivers d LEFT JOIN users u ON u.id = d.user_id`)
	if err != nil {
		log.Fatalf("load drivers: %v", err)
	}
	for rows.Next() {
		var id string
		var di driverInfo
		if err := rows.Scan(&id, &di.email, &di.verified, &di.verifiedAt); err != nil {
			log.Fatalf("scan driver: %v", err)
		}
		drivers[strings.ToLower(id)] = di
	}
	rows.Close()

	// Existing rows, keyed by driver + canonical doc_type.
	existing := map[string]bool{}
	rows, err = conn.Query(ctx, `SELECT driver_id::text, doc_type FROM driver_documents`)
	if err != nil {
		log.Fatalf("load driver_documents: %v", err)
	}
	for rows.Next() {
		var id, dt string
		if err := rows.Scan(&id, &dt); err != nil {
			log.Fatalf("scan driver_documents: %v", err)
		}
		existing[strings.ToLower(id)+"|"+canonical(dt)] = true
	}
	rows.Close()

	// Cloudinary: driver uploads go through resource_type "auto", which lands
	// images and PDFs under "image"; "raw" is listed too in case any exist.
	var all []resource
	for _, rt := range []string{"image", "raw"} {
		rs, err := listResources(cloud, key, secret, rt)
		if err != nil {
			log.Fatalf("list cloudinary %s resources: %v", rt, err)
		}
		all = append(all, rs...)
	}

	// Newest file per driver + canonical doc_type.
	best := map[string]*candidate{}
	var skipped []string
	for _, r := range all {
		pid := r.PublicID
		if r.ResourceType == "raw" {
			pid = strings.TrimSuffix(pid, path.Ext(pid)) // raw public_ids keep their extension
		}
		m := publicIDRe.FindStringSubmatch(pid)
		if m == nil {
			skipped = append(skipped, fmt.Sprintf("unparseable public_id\t%s", r.PublicID))
			continue
		}
		driverID, docType := strings.ToLower(m[1]), m[2]
		if !validDocTypes[docType] {
			skipped = append(skipped, fmt.Sprintf("unknown doc_type %q\t%s", docType, r.SecureURL))
			continue
		}
		di, ok := drivers[driverID]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("driver %s not in drivers\t%s", driverID, r.SecureURL))
			continue
		}
		k := driverID + "|" + canonical(docType)
		if existing[k] {
			skipped = append(skipped, fmt.Sprintf("row already exists (%s %s)\t%s", di.email, docType, r.SecureURL))
			continue
		}
		created, _ := time.Parse(time.RFC3339, r.CreatedAt)
		if cur, ok := best[k]; ok && !created.After(cur.createdAt) {
			skipped = append(skipped, fmt.Sprintf("older duplicate (%s %s)\t%s", di.email, docType, r.SecureURL))
			continue
		} else if ok {
			skipped = append(skipped, fmt.Sprintf("older duplicate (%s %s)\t%s", cur.email, cur.docType, cur.res.SecureURL))
		}
		best[k] = &candidate{driverID: driverID, docType: docType, email: di.email, verified: di.verified,
			verifiedAt: di.verifiedAt, res: r, createdAt: created}
	}

	plan := make([]*candidate, 0, len(best))
	for _, c := range best {
		plan = append(plan, c)
	}
	sort.Slice(plan, func(i, j int) bool {
		if plan[i].email != plan[j].email {
			return plan[i].email < plan[j].email
		}
		return plan[i].docType < plan[j].docType
	})

	approved, flagged := 0, 0
	fmt.Println("EMAIL\tDOC_TYPE\tSTATUS\tUPLOADED_AT\tFILE_URL\tFLAG")
	for _, c := range plan {
		st := status(c)
		if st == "approved" {
			approved++
		}
		warn := unreviewedFlag(c)
		if warn != "" {
			flagged++
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", c.email, c.docType, st, c.createdAt.Format(time.RFC3339), c.res.SecureURL, warn)
	}
	fmt.Printf("\nSKIPPED (%d):\n", len(skipped))
	for _, s := range skipped {
		fmt.Println("  " + s)
	}
	fmt.Printf("\ncloudinary files: %d | rows to restore: %d (approved %d, pending %d) | approved but possibly never reviewed (⚠): %d | skipped: %d\n",
		len(all), len(plan), approved, len(plan)-approved, flagged, len(skipped))

	if !*apply {
		fmt.Println("\nDRY RUN — nothing written. Re-run with -apply to restore.")
		return
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — no-op after commit
	inserted := 0
	for _, c := range plan {
		st := status(c)
		tag, err := tx.Exec(ctx, `
			INSERT INTO driver_documents
				(driver_id, doc_type, file_url, file_name, file_size, mime_type, status,
				 reviewed_at, uploaded_at, updated_at, review_note)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
				CASE WHEN $7 = 'approved' THEN NOW() END, $8, NOW(), $9)
			ON CONFLICT (driver_id, doc_type) DO NOTHING`,
			c.driverID, c.docType, c.res.SecureURL, fileName(c), c.res.Bytes, mimeType(c.res.Format),
			st, c.createdAt, restoreNote)
		if err != nil {
			log.Fatalf("insert %s %s: %v", c.email, c.docType, err)
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("\nAPPLIED — inserted %d of %d rows (review_note=%q).\n", inserted, len(plan), restoreNote)
}

func status(c *candidate) string {
	if c.verified {
		return "approved"
	}
	return "pending"
}

// unreviewedFlag marks an approved restore whose file can't be shown to
// predate verification: docs_verified_at is only set by auto-verify, so a
// manually verified driver has none to compare against.
func unreviewedFlag(c *candidate) string {
	if status(c) != "approved" {
		return ""
	}
	if c.verifiedAt == nil {
		return "⚠ no docs_verified_at (manually verified) — review status unknown"
	}
	if c.createdAt.After(*c.verifiedAt) {
		return "⚠ uploaded after verification (" + c.verifiedAt.Format(time.RFC3339) + ")"
	}
	return ""
}

func fileName(c *candidate) string {
	if c.res.Format == "" {
		return c.docType
	}
	return c.docType + "." + c.res.Format
}

func mimeType(format string) string {
	switch strings.ToLower(format) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "pdf":
		return "application/pdf"
	}
	return ""
}

// listResources pages through the Cloudinary Admin API for every uploaded
// resource of resourceType under cloudPrefix.
func listResources(cloud, key, secret, resourceType string) ([]resource, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var out []resource
	cursor := ""
	for {
		q := url.Values{"prefix": {cloudPrefix}, "max_results": {"500"}}
		if cursor != "" {
			q.Set("next_cursor", cursor)
		}
		u := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/resources/%s/upload?%s", cloud, resourceType, q.Encode())
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(key, secret)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Resources  []resource `json:"resources"`
			NextCursor string     `json:"next_cursor"`
			Error      struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode (HTTP %d): %w", resp.StatusCode, err)
		}
		if page.Error.Message != "" {
			return nil, fmt.Errorf("cloudinary: %s", page.Error.Message)
		}
		out = append(out, page.Resources...)
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}
