package apps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/sdk"
)

// A service's status page: what it says it is doing (see docs/apps.md,
// "Status"). A service serves a JSON document at the URL its app file names;
// hostd fetches it for the menu (which talks only to hostd), keeps to its
// shape and its sizes, and answers with that. The service decides what to say;
// hostd and its menu decide how it looks.

// StatusDoc is the document, as hostd passes it on.
type StatusDoc struct {
	Title    string          `json:"title,omitempty"`
	Updated  string          `json:"updated,omitempty"`
	Sections []StatusSection `json:"sections"`
}

// StatusSection is a titled group of rows.
type StatusSection struct {
	Title string      `json:"title,omitempty"`
	Rows  []StatusRow `json:"rows"`
}

// StatusRow is one line: a title, a second line, a value on the right, and
// a short badge in a tone (positive, warning or danger).
type StatusRow struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Value    string `json:"value,omitempty"`
	Badge    string `json:"badge,omitempty"`
	Tone     string `json:"tone,omitempty"`
}

// StatusAnswer is the read's answer: the document, or why there is none.
type StatusAnswer struct {
	OK       bool       `json:"ok"`
	Document *StatusDoc `json:"document,omitempty"`
	Error    string     `json:"error,omitempty"`
}

const (
	statusTimeout  = 3 * time.Second
	statusMaxBytes = 256 << 10
	statusSections = 20
	statusRows     = 100
	statusKeep     = 2 * time.Second // a page asked again sooner is the same answer
)

type statusCache struct {
	mu   sync.Mutex
	seen map[string]statusSeen
}

type statusSeen struct {
	at     time.Time
	url    string
	answer StatusAnswer
}

var statusClient = &http.Client{
	Timeout: statusTimeout,
	// An answer from somewhere else is not what the app file named.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (m *Module) readStatus(ctx context.Context, id string) (any, error) {
	app, ok := m.catalog().Get(id)
	if !ok {
		return nil, sdk.Errorf(sdk.CodeNotFound, "no app %q", id)
	}
	url := app.Status.URL
	if url == "" {
		return nil, sdk.Errorf(sdk.CodeNotFound, "%s does not say what it is doing (no status in its app file)", app.Name)
	}
	m.status.mu.Lock()
	if s, ok := m.status.seen[app.ID]; ok && s.url == url && time.Since(s.at) < statusKeep {
		m.status.mu.Unlock()
		return s.answer, nil
	}
	m.status.mu.Unlock()

	answer := fetchStatus(ctx, url)
	m.status.mu.Lock()
	if m.status.seen == nil {
		m.status.seen = map[string]statusSeen{}
	}
	m.status.seen[app.ID] = statusSeen{at: time.Now(), url: url, answer: answer}
	m.status.mu.Unlock()
	return answer, nil
}

// fetchStatus asks the service. Whatever goes wrong is an answer too (the
// page says the service does not answer), never an error of hostd's.
func fetchStatus(ctx context.Context, url string) StatusAnswer {
	if !catalog.LocalStatusURL(url) {
		return StatusAnswer{Error: "its status address is not on this machine or the home network"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StatusAnswer{Error: "its status address is not valid"}
	}
	req.Header.Set("Accept", "application/json")
	res, err := statusClient.Do(req)
	if err != nil {
		return StatusAnswer{Error: "does not answer"}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return StatusAnswer{Error: "answered " + res.Status}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, statusMaxBytes+1))
	if err != nil {
		return StatusAnswer{Error: "stopped answering"}
	}
	if len(body) > statusMaxBytes {
		return StatusAnswer{Error: "its answer is too large"}
	}
	doc, err := parseStatus(body)
	if err != nil {
		return StatusAnswer{Error: err.Error()}
	}
	return StatusAnswer{OK: true, Document: doc}
}

// parseStatus reads the document, keeping to what menus can show: known
// fields only, short texts, a bounded number of sections and rows.
func parseStatus(body []byte) (*StatusDoc, error) {
	var in StatusDoc
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errors.New("its answer is not the status document (JSON with sections)")
	}
	if in.Sections == nil {
		return nil, errors.New("its answer has no sections")
	}
	out := &StatusDoc{Title: clip(in.Title, 80), Updated: clip(in.Updated, 40), Sections: []StatusSection{}}
	for i, s := range in.Sections {
		if i == statusSections {
			break
		}
		sec := StatusSection{Title: clip(s.Title, 80), Rows: []StatusRow{}}
		for j, r := range s.Rows {
			if j == statusRows {
				break
			}
			tone := strings.ToLower(r.Tone)
			if tone != "positive" && tone != "warning" && tone != "danger" {
				tone = ""
			}
			sec.Rows = append(sec.Rows, StatusRow{Title: clip(r.Title, 120), Subtitle: clip(r.Subtitle, 200),
				Value: clip(r.Value, 80), Badge: clip(r.Badge, 24), Tone: tone})
		}
		out.Sections = append(out.Sections, sec)
	}
	return out, nil
}

// clip shortens s to n characters (not bytes), single-lined.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
