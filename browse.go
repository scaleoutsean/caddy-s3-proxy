package caddys3proxy

import (
	"bytes"
	"encoding/json"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/dustin/go-humanize"
)

const defaultBrowsePageSize = 100

func browseFormat(r *http.Request) (string, error) {
	if format := r.URL.Query().Get("format"); format != "" {
		if format == "html" || format == "json" {
			return format, nil
		}
		return "", caddyhttp.Error(http.StatusBadRequest, nil)
	}
	accept := strings.Join(r.Header.Values("Accept"), ",")
	if accept == "" {
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType == "application/json" {
			return "json", nil
		}
		return "html", nil
	}
	type preference struct {
		quality     float64
		specificity int
	}
	htmlPreference := preference{specificity: -1}
	jsonPreference := preference{specificity: -1}
	for _, value := range strings.Split(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		quality := 1.0
		if weight, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(weight, 64)
			if err != nil || !(quality >= 0 && quality <= 1) {
				continue
			}
		}
		for candidate, pref := range map[string]*preference{"text/html": &htmlPreference, "application/json": &jsonPreference} {
			specificity := -1
			switch mediaType {
			case candidate:
				specificity = 2
			case strings.SplitN(candidate, "/", 2)[0] + "/*":
				specificity = 1
			case "*/*":
				specificity = 0
			}
			if specificity >= 0 && (specificity > pref.specificity || (specificity == pref.specificity && quality > pref.quality)) {
				*pref = preference{quality: quality, specificity: specificity}
			}
		}
	}
	if htmlPreference.quality == 0 && jsonPreference.quality == 0 {
		return "", caddyhttp.Error(http.StatusNotAcceptable, nil)
	}
	if jsonPreference.quality > htmlPreference.quality ||
		(jsonPreference.quality == htmlPreference.quality && jsonPreference.specificity > htmlPreference.specificity) {
		return "json", nil
	}
	return "html", nil
}

func setBrowseHeaders(w http.ResponseWriter, format string, defaultTemplate bool) {
	w.Header().Add("Vary", "Accept, Content-Type")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if defaultTemplate && w.Header().Get("Content-Security-Policy") == "" {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		}
	}
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type PageObj struct {
	Count       int64              `json:"count"`
	Items       []Item             `json:"items"`
	MoreLink    string             `json:"more"`
	Path        string             `json:"-"`
	ParentLink  string             `json:"-"`
	Breadcrumbs []BrowseBreadcrumb `json:"-"`
}

type BrowseBreadcrumb struct {
	Name    string
	Url     string
	Current bool
}

func (po *PageObj) setNavigation(r *http.Request) {
	po.Path = r.URL.Path
	canNavigateParents := true
	if original, ok := r.Context().Value(caddyhttp.OriginalRequestCtxKey).(http.Request); ok && original.URL != nil {
		po.Path = original.URL.Path
		if !strings.HasSuffix(po.Path, r.URL.Path) {
			canNavigateParents = false
		}
	}
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) == 1 && segments[0] == "" {
		segments = nil
	}
	rootLink := strings.Repeat("../", len(segments))
	if rootLink == "" {
		rootLink = "./"
	}
	po.Breadcrumbs = []BrowseBreadcrumb{{Name: "Root", Url: rootLink, Current: len(segments) == 0}}
	for index, name := range segments {
		link := strings.Repeat("../", len(segments)-index-1)
		if link == "" {
			link = "./"
		}
		po.Breadcrumbs = append(po.Breadcrumbs, BrowseBreadcrumb{Name: name, Url: link, Current: index == len(segments)-1})
	}
	if len(segments) > 0 {
		po.ParentLink = "../"
	}
	if !canNavigateParents {
		po.Breadcrumbs = []BrowseBreadcrumb{{Name: po.Path, Url: "./", Current: true}}
		po.ParentLink = ""
	}
	query := url.Values{}
	for _, name := range []string{"format", "max"} {
		if value := r.URL.Query().Get(name); value != "" {
			query.Set(name, value)
		}
	}
	if len(query) == 0 {
		return
	}
	suffix := "?" + query.Encode()
	for index := range po.Breadcrumbs {
		po.Breadcrumbs[index].Url += suffix
	}
	if po.ParentLink != "" {
		po.ParentLink += suffix
	}
	for index := range po.Items {
		if po.Items[index].IsDir {
			po.Items[index].Url += suffix
		}
	}
	if po.MoreLink != "" {
		nextURL, _ := url.Parse(po.MoreLink)
		nextQuery := nextURL.Query()
		for name, values := range query {
			nextQuery.Set(name, values[0])
		}
		nextURL.RawQuery = nextQuery.Encode()
		po.MoreLink = nextURL.String()
	}
}

type Item struct {
	Name         string `json:"name"`
	IsDir        bool   `json:"is_dir"`
	Key          string `json:"key"`
	Url          string `json:"url"`
	Size         string `json:"size"`
	LastModified string `json:"last_modified"`
}

// GenerateJson generates JSON output for the PageObj
func (po PageObj) GenerateJson(w http.ResponseWriter) error {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	err := json.NewEncoder(buf).Encode(po)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}

func (p S3Proxy) ConstructListObjInput(r *http.Request, key string) s3.ListObjectsV2Input {
	// We need to strip the first '/' from the key to make it a valid prefix
	prefix := strings.TrimPrefix(key, "/")

	input := s3.ListObjectsV2Input{
		Bucket:    aws.String(p.Bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int64(defaultBrowsePageSize),
	}

	nextToken := r.URL.Query().Get("next")
	if nextToken != "" {
		input.ContinuationToken = aws.String(nextToken)
	}

	maxPerPage := r.URL.Query().Get("max")
	if maxPerPage != "" {
		maxKeys, err := strconv.ParseInt(maxPerPage, 10, 64)
		if err == nil && maxKeys > 0 && maxKeys <= 1000 {
			input.MaxKeys = aws.Int64(maxKeys)
		}
	}

	return input
}

// GenerateHtml generates html output for the PageObj
func (po PageObj) GenerateHtml(w http.ResponseWriter, template *template.Template) error {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	err := template.Execute(buf, po)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}

func (p S3Proxy) MakePageObj(result *s3.ListObjectsV2Output) PageObj {
	po := PageObj{Items: []Item{}}
	if result.NextContinuationToken != nil {
		var nextUrl url.URL
		queryItems := nextUrl.Query()

		queryItems.Add("next", *result.NextContinuationToken)
		if result.MaxKeys != nil {
			queryItems.Add("max", strconv.FormatInt(*result.MaxKeys, 10))
		}
		nextUrl.RawQuery = queryItems.Encode()
		po.MoreLink = nextUrl.String()
	}

	for _, dir := range result.CommonPrefixes {
		key := aws.StringValue(dir.Prefix)
		name, ok := p.browseItemName(key, result.Prefix, true)
		if !ok {
			continue
		}
		dirPath := "./" + url.PathEscape(name) + "/"
		po.Items = append(po.Items, Item{
			Url:   dirPath,
			Name:  name,
			IsDir: true,
		})
	}
	for _, obj := range result.Contents {
		key := aws.StringValue(obj.Key)
		name, ok := p.browseItemName(key, result.Prefix, false)
		if !ok {
			continue
		}
		itemPath := "./" + url.PathEscape(name)
		size := humanize.Bytes(uint64(aws.Int64Value(obj.Size)))
		timeAgo := ""
		if obj.LastModified != nil {
			timeAgo = humanize.Time(*obj.LastModified)
		}
		po.Items = append(po.Items, Item{
			Name:         name,
			Key:          key,
			Url:          itemPath,
			Size:         size,
			LastModified: timeAgo,
			IsDir:        false,
		})
	}
	po.Count = int64(len(po.Items))

	return po
}

func (p S3Proxy) browseItemName(key string, prefix *string, isDir bool) (string, bool) {
	if fileHidden(key, p.Hide) || fileHidden("/"+strings.TrimPrefix(key, "/"), p.Hide) {
		return "", false
	}
	name := path.Base(key)
	if prefix != nil {
		if !strings.HasPrefix(key, *prefix) {
			return "", false
		}
		name = strings.TrimPrefix(key, *prefix)
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}
	}
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || (!isDir && strings.HasSuffix(key, "/")) {
		return "", false
	}
	return name, true
}

const defaultBrowseTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Index of {{ .Path }}</title>
<style>
:root { color-scheme: light; --ink: #202721; --muted: #59655e; --line: #dce3df; --accent: #186344; }
* { box-sizing: border-box; }
body { margin: 0; color: var(--ink); background: #fff; font: 16px/1.5 "Segoe UI", sans-serif; }
main { max-width: 1100px; margin: 0 auto; padding: 32px 24px; }
nav { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--muted); font-size: 14px; }
nav span[aria-hidden] { color: #85948b; }
a { color: var(--accent); text-underline-offset: 3px; overflow-wrap: anywhere; }
a:focus-visible { outline: 3px solid var(--accent); outline-offset: 3px; }
h1 { margin: 20px 0 24px; font-size: 24px; font-weight: 600; overflow-wrap: anywhere; }
table { width: 100%; border-collapse: collapse; table-layout: fixed; }
th, td { padding: 12px 10px; text-align: left; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
th { color: var(--muted); font-size: 13px; font-weight: 600; background: #f3f6f4; }
th:first-child { width: 62%; }
th:nth-child(2) { width: 14%; }
th:nth-child(3) { width: 24%; }
tbody tr:hover, tbody tr:focus-within { background: #f3f6f4; }
td:first-child a { display: inline-flex; align-items: baseline; gap: 10px; max-width: 100%; }
.kind { flex: none; color: var(--muted); font: 12px/1.5 monospace; }
.name { min-width: 0; overflow-wrap: anywhere; }
.size, .modified { color: var(--muted); font-size: 14px; }
footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 16px; padding-top: 18px; font-size: 14px; }
footer p { margin: 0; color: var(--muted); }
@media (max-width: 540px) {
 main { padding: 20px 12px; }
 th, td { padding: 10px 6px; }
 th:first-child { width: 54%; }
 th:nth-child(2) { width: 18%; }
 th:nth-child(3) { width: 28%; }
 td:first-child a { gap: 5px; }
 .kind { font-size: 10px; }
 .size, .modified { font-size: 12px; }
}
</style>
</head>
<body>
<main>
<nav aria-label="Breadcrumb">
{{- range $index, $crumb := .Breadcrumbs }}
{{- if $index }}<span aria-hidden="true">/</span>{{ end }}
<a href="{{ .Url }}"{{ if .Current }} aria-current="page"{{ end }}>{{ .Name }}</a>
{{- end }}
</nav>
<h1>Index of {{ .Path }}</h1>
<table aria-label="Directory listing">
<thead><tr><th scope="col">Name</th><th scope="col">Size</th><th scope="col">Modified</th></tr></thead>
<tbody>
{{- if .ParentLink }}
<tr><td><a href="{{ .ParentLink }}"><span class="kind" aria-hidden="true">[UP]</span><span class="name">Parent directory</span></a></td><td></td><td></td></tr>
{{- end }}
{{- range .Items }}
<tr>
<td><a href="{{ .Url }}"><span class="kind" aria-hidden="true">{{ if .IsDir }}[DIR]{{ else }}[FILE]{{ end }}</span><span class="name">{{ .Name }}{{ if .IsDir }}/{{ end }}</span></a></td>
<td class="size">{{ if not .IsDir }}{{ .Size }}{{ end }}</td>
<td class="modified">{{ .LastModified }}</td>
</tr>
{{- else }}
<tr><td colspan="3">{{ if .MoreLink }}No visible entries on this page.{{ else }}No visible entries.{{ end }}</td></tr>
{{- end }}
</tbody>
</table>
<footer><p>{{ .Count }} {{ if eq .Count 1 }}entry{{ else }}entries{{ end }} on this page</p>{{ if .MoreLink }}<a href="{{ .MoreLink }}" rel="next">Next page</a>{{ end }}</footer>
</main>
</body>
</html>`
