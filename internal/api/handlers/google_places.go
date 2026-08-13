package handlers

// Server-side proxy to Google Places API (New) — the address-search
// autocomplete/details provider for LocationInput.tsx's Dispatch From/Ship
// To fields (and any other panel using that component), swapped in for Ola
// Places specifically because Ola's autocomplete ranking breaks down on
// plot-number + locality queries (confirmed via direct API testing — see
// the Ola Maps investigation). Map tiles and the pin/marker display stay on
// Ola (OlaMap.tsx) — this proxy only ever returns text + coordinates, which
// OlaMap already consumes as plain numbers regardless of provider.
//
// Same "keep the vendor key off every frontend bundle, proxy server-side"
// pattern as ReverseGeocodeProxy/ForwardGeocodeProxy in live_map.go (that
// file's Ola-Maps sibling implementation) — GOOGLE_PLACES_KEY is
// backend-only (no NEXT_PUBLIC_/EXPO_PUBLIC_ prefix, never shipped to a
// client bundle) and both endpoints below degrade to an empty result (200
// OK, never an error) if the key isn't configured yet or any upstream call
// fails, so the frontend dropdown/pin flow just shows nothing rather than
// surfacing an error.
//
// Response shapes are pre-normalized to exactly match OlaSuggestion[] /
// {lat,lng} — not Google's raw wire format — so
// bogie-tracker-panel/lib/googlePlaces.ts is a near-passthrough with zero
// mapping logic duplicated on the frontend, and LocationInput.tsx/OlaMap.tsx
// needed no logic changes beyond the import swap.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// GooglePlaceSuggestion mirrors OlaSuggestion's field names/types exactly.
type GooglePlaceSuggestion struct {
	Description string   `json:"description"`
	PlaceID     string   `json:"place_id"`
	Lat         *float64 `json:"lat"`
	Lng         *float64 `json:"lng"`
}

// GET /gogoo/places/autocomplete?input=<text>&lat=<bias lat>&lng=<bias lng>
// lat/lng are optional — when present, biases (not restricts) results
// toward that point via a 50km locationBias circle, same "nudge, don't
// filter" role the Ola integration's location= param plays today.
func GooglePlacesAutocomplete(c *gin.Context) {
	empty := gin.H{"predictions": []GooglePlaceSuggestion{}}

	input := c.Query("input")
	if input == "" {
		c.JSON(http.StatusOK, empty)
		return
	}

	apiKey := os.Getenv("GOOGLE_PLACES_KEY")
	if apiKey == "" {
		c.JSON(http.StatusOK, empty)
		return
	}

	reqBody := map[string]any{
		"input":               input,
		"includedRegionCodes": []string{"in"},
	}
	if lat, errLat := strconv.ParseFloat(c.Query("lat"), 64); errLat == nil {
		if lng, errLng := strconv.ParseFloat(c.Query("lng"), 64); errLng == nil {
			reqBody["locationBias"] = map[string]any{
				"circle": map[string]any{
					"center": map[string]float64{"latitude": lat, "longitude": lng},
					"radius": 50000,
				},
			}
		}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}

	req, err := http.NewRequest(http.MethodPost, "https://places.googleapis.com/v1/places:autocomplete", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusOK, empty)
		return
	}

	// Places API (New) autocomplete never embeds coordinates in the
	// suggestion itself (unlike Ola, which sometimes does) — every
	// selection is resolved via GooglePlaceDetails below, same two-step
	// flow LocationInput.tsx's selectSuggestion() already implements for
	// Ola's place_id-without-coords case.
	var result struct {
		Suggestions []struct {
			PlacePrediction struct {
				PlaceID string `json:"placeId"`
				Text    struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"placePrediction"`
		} `json:"suggestions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}

	predictions := make([]GooglePlaceSuggestion, 0, len(result.Suggestions))
	for _, s := range result.Suggestions {
		if s.PlacePrediction.PlaceID == "" || s.PlacePrediction.Text.Text == "" {
			continue
		}
		predictions = append(predictions, GooglePlaceSuggestion{
			Description: s.PlacePrediction.Text.Text,
			PlaceID:     s.PlacePrediction.PlaceID,
		})
	}

	c.JSON(http.StatusOK, gin.H{"predictions": predictions})
}

// GET /gogoo/places/details?place_id=<id>
// Resolves a place_id (from GooglePlacesAutocomplete above) to coordinates.
func GooglePlaceDetails(c *gin.Context) {
	empty := gin.H{"lat": nil, "lng": nil}

	placeID := c.Query("place_id")
	if placeID == "" {
		c.JSON(http.StatusOK, empty)
		return
	}

	apiKey := os.Getenv("GOOGLE_PLACES_KEY")
	if apiKey == "" {
		c.JSON(http.StatusOK, empty)
		return
	}

	req, err := http.NewRequest(http.MethodGet, "https://places.googleapis.com/v1/places/"+placeID, nil)
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}
	req.Header.Set("X-Goog-Api-Key", apiKey)
	req.Header.Set("X-Goog-FieldMask", "location")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusOK, empty)
		return
	}

	var result struct {
		Location struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}

	c.JSON(http.StatusOK, gin.H{"lat": result.Location.Latitude, "lng": result.Location.Longitude})
}
