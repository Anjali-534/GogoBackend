package handlers

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var allowedMimeTypes = map[string]string{
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/png":       ".png",
	"application/pdf": ".pdf",
}

const maxFileSize = 10 * 1024 * 1024

var requiredDocs = map[string][]string{
	"common": {
		"passport_photo",
		"aadhaar",
		"pan_card",
		"driving_license",
		"bank_passbook",
	},
	"truck": {
		"rc", "insurance", "puc",
		"fitness", "permit",
		"vehicle_photo", "vehicle_photo_side",
	},
	"two_wheeler": {
		"rc", "insurance", "puc", "vehicle_photo",
	},
	"packers": {
		"gst_cert", "goods_insurance", "vehicle_photo",
	},
	"ambulance": {
		"rc", "insurance", "emt_cert",
		"vehicle_photo", "vehicle_photo_side",
	},
}

var docLabels = map[string]string{
	"passport_photo":        "Passport Size Photo",
	"aadhaar":               "Aadhaar Card",
	"aadhaar_front":         "Aadhaar Card (Front)",
	"aadhaar_back":          "Aadhaar Card (Back)",
	"pan_card":              "PAN Card",
	"driving_license":       "Driving License",
	"driving_license_front": "Driving License (Front)",
	"driving_license_back":  "Driving License (Back)",
	"rc":                    "Registration Certificate (RC)",
	"rc_front":              "RC Book (Front)",
	"rc_back":               "RC Book (Back)",
	"insurance":             "Insurance Certificate",
	"puc":                   "Pollution Certificate (PUC)",
	"pollution_cert":        "Pollution Certificate (PUC)",
	"fitness":               "Fitness Certificate",
	"fitness_cert":          "Fitness Certificate",
	"permit":                "Vehicle Permit",
	"national_permit":       "National Permit",
	"gst_cert":              "GST Certificate",
	"emt_cert":              "EMT / Paramedic Certificate",
	"goods_insurance":       "Goods Insurance Certificate",
	"bank_passbook":         "Bank Passbook / Cancelled Cheque",
	"vehicle_photo":         "Vehicle Photo",
	"vehicle_photo_front":   "Vehicle Photo (Front)",
	"vehicle_photo_side":    "Vehicle Photo (Side)",
}

func getVehicleCategory(vehicleType string) string {
	// The app now sends the category code directly.
	switch vehicleType {
	case "two_wheeler":
		return "two_wheeler"
	case "truck_city", "truck_outstation":
		return "truck"
	case "packers":
		return "packers"
	case "ambulance":
		return "ambulance"
	}
	// Fallback for legacy / free-form labels.
	if strings.HasPrefix(vehicleType, "truck") || vehicleType == "tata_ace" || vehicleType == "bolero_pickup" {
		return "truck"
	}
	if strings.Contains(vehicleType, "bike") || strings.Contains(vehicleType, "scooter") {
		return "two_wheeler"
	}
	if strings.HasPrefix(vehicleType, "packers") || strings.Contains(vehicleType, "bhk") {
		return "packers"
	}
	if strings.Contains(vehicleType, "ambulance") || strings.Contains(vehicleType, "life_support") {
		return "ambulance"
	}
	return "two_wheeler"
}

// uploadToCloudinary uploads a file to Cloudinary using only stdlib HTTP.
// Returns the permanent secure_url. Requires CLOUDINARY_CLOUD_NAME,
// CLOUDINARY_API_KEY, and CLOUDINARY_API_SECRET environment variables.
func uploadToCloudinary(ctx context.Context, reader io.Reader, origName, docType, driverID string) (string, error) {
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	publicID := fmt.Sprintf("gogoo/drivers/%s/%s_%s", driverID, docType, uuid.New().String()[:8])

	// Cloudinary signature: SHA-1("public_id=...&timestamp=...{api_secret}")
	sigStr := fmt.Sprintf("public_id=%s&timestamp=%s%s", publicID, timestamp, apiSecret)
	h := sha1.New()
	h.Write([]byte(sigStr))
	signature := hex.EncodeToString(h.Sum(nil))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("api_key", apiKey)
	_ = mw.WriteField("timestamp", timestamp)
	_ = mw.WriteField("public_id", publicID)
	_ = mw.WriteField("signature", signature)
	fw, err := mw.CreateFormFile("file", origName)
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(fw, reader); err != nil {
		return "", err
	}
	mw.Close()

	uploadURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/auto/upload", cloudName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		SecureURL string `json:"secure_url"`
		Error     struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Error.Message != "" {
		return "", fmt.Errorf("cloudinary: %s", result.Error.Message)
	}
	return result.SecureURL, nil
}

// deleteFromCloudinary does a best-effort delete of a Cloudinary file.
// Parses resource_type and public_id from the secure_url.
func deleteFromCloudinary(fileURL string) {
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")
	if cloudName == "" {
		return
	}

	// URL: https://res.cloudinary.com/{cloud}/{resource_type}/upload/v{ver}/{public_id}.{ext}
	parts := strings.SplitN(fileURL, "/upload/", 2)
	if len(parts) != 2 {
		return
	}
	resourceType := "image"
	if strings.Contains(parts[0], "/raw/") {
		resourceType = "raw"
	} else if strings.Contains(parts[0], "/video/") {
		resourceType = "video"
	}

	// Strip version segment (e.g. "v1234567890/") then extension
	afterUpload := parts[1]
	slashIdx := strings.Index(afterUpload, "/")
	if slashIdx < 0 {
		return
	}
	publicIDWithExt := afterUpload[slashIdx+1:]
	publicID := strings.TrimSuffix(publicIDWithExt, filepath.Ext(publicIDWithExt))

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sigStr := fmt.Sprintf("public_id=%s&timestamp=%s%s", publicID, timestamp, apiSecret)
	hh := sha1.New()
	hh.Write([]byte(sigStr))
	signature := hex.EncodeToString(hh.Sum(nil))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("public_id", publicID)
	_ = mw.WriteField("api_key", apiKey)
	_ = mw.WriteField("timestamp", timestamp)
	_ = mw.WriteField("signature", signature)
	mw.Close()

	destroyURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/%s/destroy", cloudName, resourceType)
	req, err := http.NewRequest(http.MethodPost, destroyURL, &body)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	http.DefaultClient.Do(req) //nolint:errcheck — best-effort
}

// GET /gogoo/driver/profile
func GetDriverProfile(c *gin.Context) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var driverID, phone, vehicleType, vehicleNumber, vehicleModel string
	var isVerified, isOnline, isBlocked, isWalletBlocked bool
	var rating, walletBalance float64
	var totalRides int
	var walletBlockedReason *string

	err := pool.QueryRow(ctx, `
		SELECT id, COALESCE(phone,''), COALESCE(vehicle_type,''),
			COALESCE(vehicle_number,''), COALESCE(vehicle_model,''),
			is_verified, is_online, COALESCE(rating,0), COALESCE(total_rides,0),
			COALESCE(is_blocked, false), COALESCE(is_wallet_blocked, false),
			COALESCE(wallet_balance, -700.00), wallet_blocked_reason
		FROM drivers WHERE user_id=$1
	`, userID).Scan(&driverID, &phone, &vehicleType, &vehicleNumber,
		&vehicleModel, &isVerified, &isOnline, &rating, &totalRides,
		&isBlocked, &isWalletBlocked, &walletBalance, &walletBlockedReason)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver profile not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"driver_id":             driverID,
		"user_id":               userID,
		"phone":                 phone,
		"vehicle_type":          vehicleType,
		"vehicle_number":        vehicleNumber,
		"vehicle_model":         vehicleModel,
		"is_verified":           isVerified,
		"is_online":             isOnline,
		"rating":                rating,
		"total_rides":           totalRides,
		"is_blocked":            isBlocked,
		"is_wallet_blocked":     isWalletBlocked,
		"wallet_balance":        walletBalance,
		"wallet_blocked_reason": walletBlockedReason,
	})
}

// GET /gogoo/drivers/:id/documents
func GetDriverDocuments(c *gin.Context) {
	driverID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var vehicleType string
	pool.QueryRow(ctx, "SELECT vehicle_type FROM drivers WHERE id=$1", driverID).Scan(&vehicleType)

	rows, err := pool.Query(ctx, `
		SELECT doc_type, file_url, COALESCE(file_name,''), COALESCE(file_size,0),
			COALESCE(mime_type,''), status, COALESCE(reject_reason,''),
			COALESCE(doc_number,''), COALESCE(expiry_date,''), uploaded_at
		FROM driver_documents WHERE driver_id=$1
	`, driverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	uploaded := map[string]map[string]interface{}{}
	for rows.Next() {
		var docType, fileURL, fileName, mimeType, status, rejectReason, docNumber, expiryDate string
		var fileSize int
		var uploadedAt time.Time
		rows.Scan(&docType, &fileURL, &fileName, &fileSize, &mimeType, &status, &rejectReason,
			&docNumber, &expiryDate, &uploadedAt)
		uploaded[docType] = map[string]interface{}{
			"file_url":      fileURL,
			"file_name":     fileName,
			"file_size":     fileSize,
			"mime_type":     mimeType,
			"status":        status,
			"reject_reason": rejectReason,
			"doc_number":    docNumber,
			"expiry_date":   expiryDate,
			"uploaded_at":   uploadedAt,
		}
	}

	category := getVehicleCategory(vehicleType)
	allRequired := append(requiredDocs["common"], requiredDocs[category]...)

	var docs []map[string]interface{}
	seen := map[string]bool{}
	for _, docType := range allRequired {
		doc := map[string]interface{}{
			"doc_type": docType,
			"label":    docLabels[docType],
			"required": true,
			"uploaded": false,
			"status":   "missing",
		}
		if u, ok := uploaded[docType]; ok {
			doc["uploaded"] = true
			for k, v := range u {
				doc[k] = v
			}
		}
		docs = append(docs, doc)
		seen[docType] = true
	}

	// Include any uploaded docs that aren't in the required template
	// (e.g. the app uses 'aadhaar' where the template lists 'aadhaar_front').
	// An admin must still be able to see and review them.
	for docType, u := range uploaded {
		if seen[docType] {
			continue
		}
		doc := map[string]interface{}{
			"doc_type": docType,
			"label":    docLabels[docType],
			"required": false,
			"uploaded": true,
		}
		for k, v := range u {
			doc[k] = v
		}
		docs = append(docs, doc)
	}

	c.JSON(http.StatusOK, gin.H{
		"driver_id": driverID,
		"category":  category,
		"docs":      docs,
	})
}

// POST /gogoo/drivers/:id/documents  (multipart/form-data)
func UploadDriverDocument(c *gin.Context) {
	driverID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Guard: driver must exist before we write files to disk for them.
	var exists bool
	pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM drivers WHERE id=$1)", driverID).Scan(&exists)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
		return
	}

	if err := c.Request.ParseMultipartForm(maxFileSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large — max 10MB allowed"})
		return
	}

	docType := c.PostForm("doc_type")
	if docType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "doc_type is required"})
		return
	}
	if _, ok := docLabels[docType]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid doc_type"})
		return
	}

	// Optional metadata typed on the registration form.
	docNumber := c.PostForm("doc_number")
	expiryDate := c.PostForm("expiry_date")

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	mimeType := header.Header.Get("Content-Type")
	// Strip charset if present (e.g. "image/jpeg; charset=utf-8")
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	ext, allowed := allowedMimeTypes[mimeType]
	if !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only JPG, PNG and PDF files allowed"})
		return
	}

	if header.Size > maxFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file must be under 10MB"})
		return
	}

	var fileURL string
	var written int64

	if os.Getenv("CLOUDINARY_CLOUD_NAME") != "" {
		// Production: stream directly to Cloudinary — survives Railway redeploys
		secureURL, err := uploadToCloudinary(ctx, file, header.Filename, docType, driverID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud storage error: " + err.Error()})
			return
		}
		fileURL = secureURL
		written = header.Size
	} else {
		// Local dev: save to disk
		uploadDir := filepath.Join("uploads", "drivers", driverID)
		if err := os.MkdirAll(uploadDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage error"})
			return
		}
		localName := fmt.Sprintf("%s_%s%s", docType, uuid.New().String()[:8], ext)
		filePath := filepath.Join(uploadDir, localName)
		dst, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
			return
		}
		written, err = io.Copy(dst, file)
		dst.Close()
		if err != nil {
			os.Remove(filePath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write file"})
			return
		}
		fileURL = fmt.Sprintf("/uploads/drivers/%s/%s", driverID, localName)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO driver_documents
			(id, driver_id, doc_type, file_url, file_name, file_size, mime_type, doc_number, expiry_date, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending')
		ON CONFLICT (driver_id, doc_type)
		DO UPDATE SET file_url=$4, file_name=$5, file_size=$6, mime_type=$7,
			doc_number=$8, expiry_date=$9,
			status='pending', reject_reason=NULL, uploaded_at=NOW(), updated_at=NOW()
	`, uuid.New(), driverID, docType, fileURL, header.Filename, written, mimeType,
		nullIfEmpty(docNumber), nullIfEmpty(expiryDate))

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	pool.Exec(ctx, "UPDATE drivers SET docs_submitted=true, updated_at=NOW() WHERE id=$1", driverID)

	c.JSON(http.StatusOK, gin.H{
		"doc_type":  docType,
		"label":     docLabels[docType],
		"file_url":  fileURL,
		"file_name": header.Filename,
		"file_size": written,
		"mime_type": mimeType,
		"status":    "pending",
		"message":   "Document uploaded successfully",
	})
}

// PATCH /gogoo/drivers/:id/documents/:doc_type/review
func ReviewDriverDocument(c *gin.Context) {
	driverID := c.Param("id")
	docType := c.Param("doc_type")

	// reviewerID stays nil if no user — NULL is fine for reviewed_by

	var req struct {
		Status       string `json:"status" binding:"required"`
		RejectReason string `json:"reject_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be approved or rejected"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `
    UPDATE driver_documents
    SET status=$1, reject_reason=$2, reviewed_at=NOW(), updated_at=NOW()
    WHERE driver_id=$3 AND doc_type=$4
`, req.Status, nullIfEmpty(req.RejectReason), driverID, docType)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update document"})
		return
	}

	var pendingCount int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_documents WHERE driver_id=$1 AND status != 'approved'
	`, driverID).Scan(&pendingCount)

	if pendingCount == 0 {
		pool.Exec(ctx, `
			UPDATE drivers SET is_verified=true, docs_verified_at=NOW(), updated_at=NOW() WHERE id=$1
		`, driverID)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "Document reviewed",
		"status":         req.Status,
		"all_docs_clear": pendingCount == 0,
	})
}

// DELETE /gogoo/drivers/:id/documents/:doc_type
func DeleteDriverDocument(c *gin.Context) {
	driverID := c.Param("id")
	docType := c.Param("doc_type")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var fileURL string
	pool.QueryRow(ctx,
		"SELECT file_url FROM driver_documents WHERE driver_id=$1 AND doc_type=$2",
		driverID, docType).Scan(&fileURL)

	if fileURL != "" {
		if strings.HasPrefix(fileURL, "https://res.cloudinary.com") {
			deleteFromCloudinary(fileURL)
		} else {
			os.Remove("." + fileURL)
		}
	}
	pool.Exec(ctx,
		"DELETE FROM driver_documents WHERE driver_id=$1 AND doc_type=$2",
		driverID, docType)

	c.JSON(http.StatusOK, gin.H{"message": "Document deleted"})
}
