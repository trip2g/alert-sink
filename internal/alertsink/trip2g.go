// trip2g write client.
//
// Auth model (self-mint): the sink holds the alerts instance's JWT secret,
// self-signs a 5 minute HAT (hot auth token) JWT with claims e (email) and
// ae (admin enter), POSTs it to /_system/hat, harvests the session cookie
// from the response, and re-sends that token as Authorization: Bearer on
// /_system/graphql. No other party is involved in minting credentials.
package alertsink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const maxRespBody = 4 << 20

// hatClaims mirrors trip2g's hot auth token claims.
type hatClaims struct {
	Email      string `json:"e"`
	AdminEnter bool   `json:"ae,omitempty"`
	jwt.RegisteredClaims
}

// SignHAT self-signs a 5 minute HAT JWT for the given email with the
// instance JWT secret (HS256).
func SignHAT(jwtSecret, email string, now time.Time) (string, error) {
	claims := hatClaims{
		Email:      email,
		AdminEnter: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
}

// Trip2gClient writes notes into one trip2g instance.
type Trip2gClient struct {
	baseURL   string
	jwtSecret string
	email     string
	hc        *http.Client
}

// NewTrip2gClient returns a client for the instance at baseURL.
func NewTrip2gClient(baseURL, jwtSecret, email string, timeout time.Duration) *Trip2gClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Trip2gClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		jwtSecret: jwtSecret,
		email:     email,
		hc: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// session exchanges a freshly minted HAT at /_system/hat and returns the
// session token harvested from Set-Cookie.
func (c *Trip2gClient) session(ctx context.Context) (string, error) {
	token, err := SignHAT(c.jwtSecret, c.email, time.Now())
	if err != nil {
		return "", fmt.Errorf("sign HAT: %w", err)
	}

	body := url.Values{"token": {token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/_system/hat", strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build HAT request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("HAT exchange: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRespBody))

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HAT exchange returned http %d", resp.StatusCode)
	}

	sess := harvestSessionCookie(resp.Cookies())
	if sess == "" {
		return "", errors.New("HAT exchange set no session cookie")
	}
	return sess, nil
}

// harvestSessionCookie prefers a cookie named "session", else the first
// non-empty cookie.
func harvestSessionCookie(cookies []*http.Cookie) string {
	first := ""
	for _, ck := range cookies {
		if ck.Value == "" {
			continue
		}
		if ck.Name == "session" {
			return ck.Value
		}
		if first == "" {
			first = ck.Value
		}
	}
	return first
}

// GraphQL request/response plumbing for the updateNotes mutation.

const updateNotesMutation = `mutation UpdateNotes($changes: [NoteChangeInput!]!) {
  updateNotes(input: { changes: $changes }) {
    __typename
    ... on UpdateNotesSuccessPayload { paths }
    ... on UpdateNotesHashMismatchPayload { path actualHash }
    ... on UpdateNotesPatchNotFoundPayload { path find }
    ... on ErrorPayload { message }
  }
}`

// NoteUpsert is the NoteChangeInput.upsert shape.
type NoteUpsert struct {
	Path         string  `json:"path"`
	Content      string  `json:"content"`
	ExpectedHash *string `json:"expectedHash,omitempty"`
}

// NotePatch is the NoteChangeInput.patch shape: a single find/replace inside
// the existing note content.
type NotePatch struct {
	Path         string  `json:"path"`
	Find         string  `json:"find"`
	Replace      string  `json:"replace"`
	ExpectedHash *string `json:"expectedHash,omitempty"`
}

// NoteChange is one entry of the updateNotes changes list.
type NoteChange struct {
	Upsert *NoteUpsert `json:"upsert,omitempty"`
	Patch  *NotePatch  `json:"patch,omitempty"`
}

type updateNotesResult struct {
	Typename   string   `json:"__typename"`
	Paths      []string `json:"paths"`
	Path       string   `json:"path"`
	ActualHash string   `json:"actualHash"`
	Find       string   `json:"find"`
	Message    string   `json:"message"`
}

// Typed outcomes of UpdateNotes, so callers can branch on them.
var (
	ErrHashMismatch  = errors.New("trip2g: hash mismatch")
	ErrPatchNotFound = errors.New("trip2g: patch find string not found")
	ErrNoteNotFound  = errors.New("trip2g: note not found")
)

// graphql POSTs one operation under a fresh HAT session and unmarshals the
// response envelope's data field into out.
func (c *Trip2gClient) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	sess, err := c.session(ctx)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/_system/graphql", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return fmt.Errorf("read graphql response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql endpoint returned http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err = json.Unmarshal(body, &envelope)
	if err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", envelope.Errors[0].Message)
	}
	if out != nil && len(envelope.Data) > 0 {
		err = json.Unmarshal(envelope.Data, out)
		if err != nil {
			return fmt.Errorf("unmarshal graphql data: %w", err)
		}
	}
	return nil
}

// UpdateNotes runs the updateNotes mutation with the given changes under a
// fresh HAT session.
func (c *Trip2gClient) UpdateNotes(ctx context.Context, changes []NoteChange) error {
	var data struct {
		UpdateNotes updateNotesResult `json:"updateNotes"`
	}
	err := c.graphql(ctx, updateNotesMutation, map[string]any{"changes": changes}, &data)
	if err != nil {
		return err
	}

	res := data.UpdateNotes
	switch res.Typename {
	case "UpdateNotesSuccessPayload":
		return nil
	case "UpdateNotesHashMismatchPayload":
		return fmt.Errorf("%w: %s", ErrHashMismatch, res.Path)
	case "UpdateNotesPatchNotFoundPayload":
		return fmt.Errorf("%w: %s", ErrPatchNotFound, res.Path)
	case "ErrorPayload":
		if strings.Contains(res.Message, "note not found") {
			return fmt.Errorf("%w: %s", ErrNoteNotFound, res.Message)
		}
		return fmt.Errorf("updateNotes: %s", res.Message)
	default:
		return fmt.Errorf("updateNotes: unexpected payload %q", res.Typename)
	}
}

const createReleaseMutation = `mutation CreateRelease($title: String!) {
  admin {
    createRelease(input: { title: $title }) {
      __typename
      ... on CreateReleasePayload { release { id } }
      ... on ErrorPayload { message }
    }
  }
}`

// CreateRelease snapshots the latest note versions into a new release and
// makes it live. trip2g serves the public site from the live release, so the
// sink publishes after writing; without this, incident notes stay drafts.
func (c *Trip2gClient) CreateRelease(ctx context.Context, title string) error {
	var data struct {
		Admin struct {
			CreateRelease struct {
				Typename string `json:"__typename"`
				Message  string `json:"message"`
			} `json:"createRelease"`
		} `json:"admin"`
	}
	err := c.graphql(ctx, createReleaseMutation, map[string]any{"title": title}, &data)
	if err != nil {
		return err
	}
	res := data.Admin.CreateRelease
	switch res.Typename {
	case "CreateReleasePayload":
		return nil
	case "ErrorPayload":
		return fmt.Errorf("createRelease: %s", res.Message)
	default:
		return fmt.Errorf("createRelease: unexpected payload %q", res.Typename)
	}
}
