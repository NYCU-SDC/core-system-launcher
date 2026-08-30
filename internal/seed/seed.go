// Package seed loads the pre-exported sample forms once the backend is up.
//
// Through the API rather than raw SQL: a form spans four levels of related
// records (section, question, choice, workflow), so writing it directly means
// owning UUID and foreign-key handling that breaks whenever the schema moves.
// Going through the API lets the backend mint ids and defaults itself.
//
// The exported JSON holds no UUIDs; nodes reference each other by array index
// (see seed/export.py).
package seed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// ── Export format ────────────────────────────────────────────────

// Form is one reproducible form.
type Form struct {
	Title                  string          `json:"title"`
	PreviewMessage         string          `json:"previewMessage"`
	Description            json.RawMessage `json:"description"`
	MessageAfterSubmission string          `json:"messageAfterSubmission"`
	Visibility             string          `json:"visibility"`
	Sections               []Section       `json:"sections"`
	Workflow               []Node          `json:"workflow"`
}

// Section is one section of a form.
type Section struct {
	Title       string          `json:"title"`
	Description json.RawMessage `json:"description"`
	Questions   []Question      `json:"questions"`
}

// Question is a single question. Type-specific settings pass through as
// RawMessage so new upstream fields need no change here.
type Question struct {
	Title        string          `json:"title"`
	Type         string          `json:"type"`
	Required     bool            `json:"required"`
	Description  json.RawMessage `json:"description"`
	Choices      []Choice        `json:"choices,omitempty"`
	Scale        json.RawMessage `json:"scale,omitempty"`
	UploadFile   json.RawMessage `json:"uploadFile,omitempty"`
	Date         json.RawMessage `json:"date,omitempty"`
	OAuthConnect string          `json:"oauthConnect,omitempty"`
}

// Choice is one selectable option.
type Choice struct {
	Name        string `json:"name"`
	IsOther     bool   `json:"isOther,omitempty"`
	Description string `json:"description,omitempty"`
}

// Ref points at a question or choice by array index.
type Ref struct {
	Section  int `json:"section"`
	Question int `json:"question"`
	Choice   int `json:"choice"`
}

// Condition is a condition node's rule.
type Condition struct {
	Source   string `json:"source"`
	Question *Ref   `json:"question,omitempty"`
	Choice   *Ref   `json:"choice,omitempty"`
	// Pattern is only set when the rule cannot be expressed as a Choice.
	Pattern string `json:"pattern,omitempty"`
}

// Node is one workflow node. The next fields hold node indices.
type Node struct {
	Type      string          `json:"type"`
	Label     string          `json:"label"`
	Payload   json.RawMessage `json:"payload"`
	Section   *int            `json:"section,omitempty"`
	Condition *Condition      `json:"condition,omitempty"`
	Next      *int            `json:"next,omitempty"`
	NextTrue  *int            `json:"nextTrue,omitempty"`
	NextFalse *int            `json:"nextFalse,omitempty"`
}

// ── API client ───────────────────────────────────────────────────

// Client is a logged-in core system API client.
type Client struct {
	base string
	http *http.Client
}

// NewClient trades a session out of the backend's internal login endpoint.
//
// That endpoint only exists when DEV=true, which is how the launcher's
// generated compose file configures it.
func NewClient(base, userID string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := &Client{base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	if _, err := c.do("POST", "/api/auth/login/internal", map[string]string{"uid": userID}); err != nil {
		return nil, fmt.Errorf("以內部端點登入失敗：%w", err)
	}
	return c, nil
}

func (c *Client) do(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s 回傳 %d：%s", method, path, resp.StatusCode, truncate(string(out), 300))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── Seeding ──────────────────────────────────────────────────────

type idOnly struct {
	ID string `json:"id"`
}

// ExistingTitles returns the titles of forms that already exist in the org.
func (c *Client) ExistingTitles(org string) (map[string]bool, error) {
	out, err := c.do("GET", "/api/orgs/"+org+"/forms", nil)
	if err != nil {
		return nil, err
	}
	var forms []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &forms); err != nil {
		return nil, err
	}
	titles := map[string]bool{}
	for _, f := range forms {
		titles[f.Title] = true
	}
	return titles, nil
}

// CreateForm creates a form and returns its id. Forms are always left as
// drafts: DRAFT is the backend's default on creation and the launcher never
// publishes anything.
func (c *Client) CreateForm(org string, f *Form) (string, error) {
	body := map[string]any{
		"title":       f.Title,
		"description": rawOrEmptyDoc(f.Description),
		"visibility":  visibilityOr(f.Visibility),
	}
	if f.PreviewMessage != "" {
		body["previewMessage"] = f.PreviewMessage
	}
	if f.MessageAfterSubmission != "" {
		body["messageAfterSubmission"] = f.MessageAfterSubmission
	}
	out, err := c.do("POST", "/api/orgs/"+org+"/forms", body)
	if err != nil {
		return "", err
	}
	var v idOnly
	if err := json.Unmarshal(out, &v); err != nil {
		return "", err
	}
	return v.ID, nil
}

// Seed builds a whole form: sections, questions and workflow.
func (c *Client) Seed(org string, f *Form) error {
	formID, err := c.CreateForm(org, f)
	if err != nil {
		return err
	}

	// Every section is a SECTION node; the node id is the section id.
	sectionIDs := make([]string, len(f.Sections))
	// questionIDs[section][question]
	questionIDs := make([][]string, len(f.Sections))
	// choiceIDs[section][question][choice]
	choiceIDs := make([][][]string, len(f.Sections))

	for i, s := range f.Sections {
		out, err := c.do("POST", fmt.Sprintf("/api/forms/%s/workflow/nodes", formID),
			map[string]any{"type": "SECTION", "payload": map[string]float64{"x": 0, "y": 0}})
		if err != nil {
			return fmt.Errorf("建立 section 節點失敗：%w", err)
		}
		var node idOnly
		if err := json.Unmarshal(out, &node); err != nil {
			return err
		}
		sectionIDs[i] = node.ID

		if _, err := c.do("PATCH", fmt.Sprintf("/api/forms/%s/sections/%s", formID, node.ID),
			map[string]any{"title": s.Title, "description": rawOrEmptyDoc(s.Description)}); err != nil {
			return fmt.Errorf("設定 section「%s」失敗：%w", s.Title, err)
		}

		questionIDs[i] = make([]string, len(s.Questions))
		choiceIDs[i] = make([][]string, len(s.Questions))
		for j, q := range s.Questions {
			body := map[string]any{
				"title":       q.Title,
				"type":        q.Type,
				"required":    q.Required,
				"order":       j + 1,
				"description": rawOrEmptyDoc(q.Description),
			}
			if len(q.Choices) > 0 {
				body["choices"] = q.Choices
			}
			if len(q.Scale) > 0 {
				body["scale"] = q.Scale
			}
			if len(q.UploadFile) > 0 {
				body["uploadFile"] = q.UploadFile
			}
			if len(q.Date) > 0 {
				body["date"] = q.Date
			}
			if q.OAuthConnect != "" {
				body["oauthConnect"] = q.OAuthConnect
			}
			out, err := c.do("POST", fmt.Sprintf("/api/sections/%s/questions", node.ID), body)
			if err != nil {
				return fmt.Errorf("建立題目「%s」失敗：%w", q.Title, err)
			}
			var created struct {
				ID      string `json:"id"`
				Choices []struct {
					ID string `json:"id"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(out, &created); err != nil {
				return err
			}
			questionIDs[i][j] = created.ID
			ids := make([]string, len(created.Choices))
			for k, ch := range created.Choices {
				ids[k] = ch.ID
			}
			choiceIDs[i][j] = ids
		}
	}

	return c.applyWorkflow(formID, f, sectionIDs, questionIDs, choiceIDs)
}

func (c *Client) applyWorkflow(formID string, f *Form, sectionIDs []string, questionIDs [][]string, choiceIDs [][][]string) error {
	// Read the current workflow back to learn the auto-created START and END ids.
	out, err := c.do("GET", fmt.Sprintf("/api/forms/%s/workflow", formID), nil)
	if err != nil {
		return err
	}
	var current struct {
		Workflow []struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Label string `json:"label"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(out, &current); err != nil {
		return err
	}

	// Export node index -> real id.
	nodeID := make([]string, len(f.Workflow))
	var startID, endID, startLabel, endLabel string
	for _, n := range current.Workflow {
		switch n.Type {
		case "START":
			startID, startLabel = n.ID, n.Label
		case "END":
			endID, endLabel = n.ID, n.Label
		}
	}
	if startID == "" || endID == "" {
		return fmt.Errorf("表單缺少 START 或 END 節點")
	}

	for i, n := range f.Workflow {
		switch n.Type {
		case "START":
			nodeID[i] = startID
		case "END":
			nodeID[i] = endID
		case "SECTION":
			if n.Section == nil || *n.Section >= len(sectionIDs) {
				return fmt.Errorf("workflow 第 %d 個節點的 section 索引無效", i)
			}
			nodeID[i] = sectionIDs[*n.Section]
		case "CONDITION":
			out, err := c.do("POST", fmt.Sprintf("/api/forms/%s/workflow/nodes", formID),
				map[string]any{"type": "CONDITION", "payload": map[string]float64{"x": 0, "y": 0}})
			if err != nil {
				return fmt.Errorf("建立條件節點失敗：%w", err)
			}
			var node idOnly
			if err := json.Unmarshal(out, &node); err != nil {
				return err
			}
			nodeID[i] = node.ID
		default:
			return fmt.Errorf("不認得的節點型別：%s", n.Type)
		}
	}

	nodes := make([]map[string]any, 0, len(f.Workflow))
	for i, n := range f.Workflow {
		item := map[string]any{"id": nodeID[i], "payload": rawOrOrigin(n.Payload)}
		switch n.Type {
		case "START":
			item["label"] = startLabel
		case "END":
			item["label"] = endLabel
		case "CONDITION":
			// The export carries no label for condition nodes: the backend
			// generates one containing the choice UUID of whichever system it
			// runs on, so exporting it would only propagate a stale id. A
			// placeholder keeps the required field populated until the backend
			// replaces it.
			item["label"] = "condition"
		default:
			item["label"] = n.Label
		}

		if n.Condition != nil {
			rule := map[string]any{"source": n.Condition.Source}
			if n.Condition.Question == nil {
				return fmt.Errorf("條件節點「%s」缺少題目參照", n.Label)
			}
			r := n.Condition.Question
			if r.Section >= len(questionIDs) || r.Question >= len(questionIDs[r.Section]) {
				return fmt.Errorf("條件節點「%s」的題目參照超出範圍", n.Label)
			}
			rule["question"] = questionIDs[r.Section][r.Question]

			switch {
			case n.Condition.Choice != nil:
				ch := n.Condition.Choice
				if ch.Section >= len(choiceIDs) || ch.Question >= len(choiceIDs[ch.Section]) ||
					ch.Choice >= len(choiceIDs[ch.Section][ch.Question]) {
					return fmt.Errorf("條件節點「%s」的選項參照超出範圍", n.Label)
				}
				// A CHOICE rule matches against option ids, so anchor the regex.
				rule["pattern"] = "^" + choiceIDs[ch.Section][ch.Question][ch.Choice] + "$"
			case n.Condition.Pattern != "":
				rule["pattern"] = n.Condition.Pattern
			default:
				return fmt.Errorf("條件節點「%s」缺少比對條件", n.Label)
			}
			item["conditionRule"] = rule
		}

		if n.Next != nil {
			item["next"] = nodeID[*n.Next]
		}
		if n.NextTrue != nil {
			item["nextTrue"] = nodeID[*n.NextTrue]
		}
		if n.NextFalse != nil {
			item["nextFalse"] = nodeID[*n.NextFalse]
		}
		nodes = append(nodes, item)
	}

	if _, err := c.do("PUT", fmt.Sprintf("/api/forms/%s/workflow", formID), nodes); err != nil {
		return fmt.Errorf("套用 workflow 失敗：%w", err)
	}
	return nil
}

// visibilityOr fills in the default.
//
// Note that visibility (PUBLIC / PRIVATE) and status (DRAFT / PUBLISHED) are
// separate things: this sets visibility, while newly created forms are always
// DRAFT. The API only accepts uppercase; the DB enum stores lowercase and the
// backend converts.
func visibilityOr(v string) string {
	if v == "" {
		return "PUBLIC"
	}
	return v
}

func rawOrEmptyDoc(r json.RawMessage) any {
	if len(r) == 0 {
		return map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph"}}}
	}
	return r
}

func rawOrOrigin(r json.RawMessage) any {
	if len(r) == 0 {
		return map[string]float64{"x": 0, "y": 0}
	}
	return r
}

// Parse reads the exported list of forms.
func Parse(data []byte) ([]Form, error) {
	var forms []Form
	if err := json.Unmarshal(data, &forms); err != nil {
		return nil, fmt.Errorf("範例表單資料格式錯誤：%w", err)
	}
	return forms, nil
}
