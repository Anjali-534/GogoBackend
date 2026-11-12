package handlers

import (
	"net/http"
	"strings"
	"testing"
)

func TestCheckTrackerDriverAction(t *testing.T) {
	cases := []struct {
		name   string
		stop   trackerDriverStop
		action trackerDriverAction
		status int // 0 = accepted
	}{
		{"in transit, reached", trackerDriverStop{Status: "in_transit"}, trackerActionReached, 0},
		{"in transit, deliver", trackerDriverStop{Status: "in_transit"}, trackerActionDeliver, 0},
		{"in transit, already signed, deliver (retry overwrite ok)", trackerDriverStop{Status: "in_transit", HasSignature: true}, trackerActionDeliver, 0},
		{"delivered by consignee, unsigned, deliver", trackerDriverStop{Status: "delivered"}, trackerActionDeliver, 0},
		{"delivered, signed, deliver", trackerDriverStop{Status: "delivered", HasSignature: true}, trackerActionDeliver, http.StatusConflict},
		{"delivered, reached", trackerDriverStop{Status: "delivered"}, trackerActionReached, http.StatusBadRequest},
		{"cancelled, deliver", trackerDriverStop{Status: "cancelled"}, trackerActionDeliver, http.StatusBadRequest},
		{"cancelled, reached", trackerDriverStop{Status: "cancelled"}, trackerActionReached, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := checkTrackerDriverAction(tc.stop, tc.action)
			got := 0
			if e != nil {
				got = e.Status
			}
			if got != tc.status {
				t.Fatalf("status = %d (%v), want %d", got, e, tc.status)
			}
		})
	}
}

func TestCloudinaryResponseError(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		body    string
		want    []string
	}{
		{"JSON error message", 401, "Invalid Signature 1a2b. String to sign - 'public_id=x&timestamp=1'.", `{"error":{"message":"..."}}`,
			[]string{"HTTP 401", "Invalid Signature"}},
		{"HTML gateway error", 502, "", "<html><body>502 Bad Gateway</body></html>",
			[]string{"HTTP 502", "502 Bad Gateway"}},
		{"empty body", 500, "", "", []string{"HTTP 500", "(empty response)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cloudinaryResponseError(tc.status, tc.message, []byte(tc.body)).Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("%q missing %q", got, w)
				}
			}
		})
	}
	long := cloudinaryResponseError(500, "", []byte(strings.Repeat("x", 5000))).Error()
	if len(long) > 400 {
		t.Fatalf("long body not truncated: %d chars", len(long))
	}
}
