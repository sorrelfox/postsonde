package report

import (
	"encoding/json"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"
)

// WriteJSON writes r as JSON to w: the record as the run history keeps it
// (Schema, the summary's members, then "results"), indented.
func WriteJSON(w io.Writer, r Run) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// WriteHTML writes r as one self-contained HTML page to w: the summary,
// then a table of the results with why each one failed. It has its own
// styles and no scripts, links to other files or remote resources, so it
// opens anywhere; html/template escapes every text.
func WriteHTML(w io.Writer, r Run) error {
	return htmlPage.Execute(w, htmlView(r))
}

type htmlResult struct {
	Result
	Where   string
	Time    string
	Details []string
}

type htmlData struct {
	Run
	Title   string
	Started string
	Elapsed string
	Status  string
	Rows    []htmlResult
	Multi   bool
}

func htmlView(r Run) htmlData {
	d := htmlData{Run: r, Title: r.Title(), Started: dateTime(r.StartedAt), Elapsed: millis(r.ElapsedMs), Multi: r.Iterations > 1}
	switch r.Status {
	case StatusStopped:
		d.Status = "Stopped at a failure"
	case StatusCancelled:
		d.Status = "Stopped"
	default:
		d.Status = "Finished"
	}
	for _, res := range r.Results {
		row := htmlResult{Result: res, Where: strings.Join(res.Path, " / "), Time: millis(res.DurationMs)}
		switch res.Outcome {
		case OutcomeFailed:
			for _, t := range res.FailedTests() {
				row.Details = append(row.Details, t.Message)
			}
			if len(row.Details) == 0 {
				row.Details = []string{res.Why()}
			}
		case OutcomeError, OutcomeSkipped:
			row.Details = []string{res.Why()}
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

func dateTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC")
}

func millis(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(max(0, ms), 10) + " ms"
	}
	return seconds(ms) + " s"
}

var htmlPage = template.Must(template.New("report").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="generator" content="PostSonde">
<title>{{.Title}}: run report</title>
<style>
:root { color-scheme: light dark; --ok: #1a7f37; --bad: #cf222e; --warn: #9a6700; --muted: #6e7781; --line: #d0d7de; }
@media (prefers-color-scheme: dark) { :root { --ok: #3fb950; --bad: #f85149; --warn: #d29922; --muted: #8b949e; --line: #30363d; } }
body { margin: 2rem; font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
h1 { margin: 0 0 .25rem; font-size: 1.4rem; }
p.meta { margin: 0 0 1rem; color: var(--muted); }
dl { display: flex; flex-wrap: wrap; gap: .5rem 2rem; margin: 0 0 1.5rem; }
dl div { display: flex; flex-direction: column; }
dt { color: var(--muted); font-size: .85rem; }
dd { margin: 0; font-weight: 600; }
table { width: 100%; border-collapse: collapse; }
th, td { padding: .35rem .5rem; border-top: 1px solid var(--line); text-align: left; vertical-align: top; }
th { color: var(--muted); font-weight: 600; }
td.num { white-space: nowrap; font-variant-numeric: tabular-nums; }
.passed { color: var(--ok); } .failed { color: var(--bad); } .error { color: var(--warn); } .skipped { color: var(--muted); }
.why { margin: .15rem 0 0; color: var(--muted); white-space: pre-wrap; }
.path { color: var(--muted); }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p class="meta">Run of {{.Started}} · {{.Status}}{{if .DataFile}} · data file {{.DataFile}}, {{.DataRows}} rows{{end}}</p>
<dl>
<div><dt>Results</dt><dd>{{.Counts.Results}}</dd></div>
<div><dt>Passed</dt><dd class="passed">{{.Counts.Passed}}</dd></div>
<div><dt>Failed</dt><dd class="failed">{{.Counts.Failed}}</dd></div>
<div><dt>Errors</dt><dd class="error">{{.Counts.Errors}}</dd></div>
<div><dt>Skipped</dt><dd class="skipped">{{.Counts.Skipped}}</dd></div>
<div><dt>Iterations</dt><dd>{{.Iterations}}</dd></div>
<div><dt>Run time</dt><dd>{{.Elapsed}}</dd></div>
</dl>
{{- if .Omitted}}
<p class="meta">{{.Omitted}} later results are not in this report.</p>
{{- end}}
<table>
<thead><tr><th scope="col">Outcome</th>{{if .Multi}}<th scope="col">Iteration</th>{{end}}<th scope="col">Request</th><th scope="col">Status</th><th scope="col">Time</th><th scope="col">Tests</th></tr></thead>
<tbody>
{{- range .Rows}}
<tr>
<td class="{{.Outcome}}">{{.Outcome}}</td>
{{- if $.Multi}}<td class="num">{{.Iteration}}</td>{{end}}
<td>{{if .Method}}{{.Method}} {{end}}{{.Name}}{{if .Where}} <span class="path">{{.Where}}</span>{{end}}{{range .Details}}<p class="why">{{.}}</p>{{end}}</td>
<td class="num">{{if .Status}}{{.Status}}{{else if .StatusCode}}{{.StatusCode}}{{end}}</td>
<td class="num">{{if ne .Outcome "skipped"}}{{.Time}}{{end}}</td>
<td class="num">{{if or .TestsPassed .TestsFailed}}{{.TestsPassed}} passed{{if .TestsFailed}}, {{.TestsFailed}} failed{{end}}{{end}}</td>
</tr>
{{- end}}
</tbody>
</table>
</body>
</html>
`))
