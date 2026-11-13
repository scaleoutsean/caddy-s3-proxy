package caddys3proxy

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

func TestDefaultBrowseTemplate(t *testing.T) {
	tpl := template.Must(template.New("listing").Parse(defaultBrowseTemplate))
	page := PageObj{Count: 1, Items: []Item{{Name: "report.txt", Url: "./report.txt"}}}
	response := httptest.NewRecorder()
	if err := page.GenerateHtml(response, tpl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), `href="./report.txt"`) {
		t.Fatalf("missing object link: %s", response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", got)
	}
}

func TestBrowseFormat(t *testing.T) {
	tests := []struct {
		name, query, accept, contentType, want string
		status                                 int
	}{
		{name: "default", want: "html"},
		{name: "wildcard", accept: "*/*", want: "html"},
		{name: "browser", accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", want: "html"},
		{name: "JSON", accept: "application/json", want: "json"},
		{name: "weighted", accept: "text/html;q=0.2,application/json;q=0.8", want: "json"},
		{name: "tie", accept: "application/json,text/html", want: "html"},
		{name: "specific JSON", accept: "application/json,*/*", want: "json"},
		{name: "HTML excluded", accept: "text/html;q=0,*/*", want: "json"},
		{name: "JSON excluded", accept: "application/json;q=0,*/*", want: "html"},
		{name: "unsupported", accept: "image/png", status: 406},
		{name: "excluded", accept: "*/*;q=0", status: 406},
		{name: "invalid weight", accept: "application/json;q=NaN", status: 406},
		{name: "out of range weight", accept: "application/json;q=2", status: 406},
		{name: "invalid format", query: "?format=xml", status: 400},
		{name: "explicit JSON", query: "?format=json", accept: "text/html", want: "json"},
		{name: "explicit HTML", query: "?format=html", accept: "application/json", want: "html"},
		{name: "legacy JSON", contentType: "application/json; charset=utf-8", want: "json"},
		{name: "Accept takes precedence", contentType: "application/json", accept: "text/html", want: "html"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/"+test.query, nil)
			request.Header.Set("Accept", test.accept)
			request.Header.Set("Content-Type", test.contentType)
			format, err := browseFormat(request)
			if test.status != 0 {
				if handlerError, ok := err.(caddyhttp.HandlerError); !ok || handlerError.StatusCode != test.status {
					t.Fatalf("want status %d, got %v", test.status, err)
				}
			} else if err != nil || format != test.want {
				t.Fatalf("want %q, got %q, %v", test.want, format, err)
			}
		})
	}
}

func TestBrowsePageSize(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1001", "invalid", "1000"} {
		t.Run(value, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/?max="+value, nil)
			input := (S3Proxy{}).ConstructListObjInput(request, "/")
			want := int64(defaultBrowsePageSize)
			if value == "1000" {
				want = 1000
			}
			if got := aws.Int64Value(input.MaxKeys); got != want {
				t.Fatalf("want max %d, got %d", want, got)
			}
		})
	}
}

func TestBrowseSecurityDefaults(t *testing.T) {
	proxy := S3Proxy{}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, handler := range []func(http.ResponseWriter, *http.Request, string) error{proxy.GetHandler, proxy.HeadHandler} {
		err := handler(httptest.NewRecorder(), request, "/")
		if handlerError, ok := err.(caddyhttp.HandlerError); !ok || handlerError.StatusCode != http.StatusForbidden {
			t.Fatalf("browsing must be disabled by default: %v", err)
		}
	}
	for _, test := range []struct {
		format          string
		defaultTemplate bool
		policy          string
		wantPolicy      bool
	}{
		{format: "html", defaultTemplate: true, wantPolicy: true},
		{format: "html", defaultTemplate: false},
		{format: "json", defaultTemplate: true},
		{format: "html", defaultTemplate: true, policy: "default-src 'none'", wantPolicy: true},
	} {
		response := httptest.NewRecorder()
		if test.policy != "" {
			response.Header().Set("Content-Security-Policy", test.policy)
		}
		setBrowseHeaders(response, test.format, test.defaultTemplate)
		policy := response.Header().Get("Content-Security-Policy")
		if (policy != "") != test.wantPolicy || (test.policy != "" && policy != test.policy) {
			t.Fatalf("unexpected CSP: %q", policy)
		}
	}
	tpl := template.Must(template.New("listing").Parse(defaultBrowseTemplate))
	response := httptest.NewRecorder()
	if err := (PageObj{MoreLink: "?next=token"}).GenerateHtml(response, tpl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "No visible entries on this page.") || !strings.Contains(response.Body.String(), `rel="next"`) {
		t.Fatal("an empty filtered page must retain continuation navigation")
	}
}

func TestBrowseVisibilityAndLinks(t *testing.T) {
	proxy := S3Proxy{Hide: []string{"private", "/backend/docs/secret.txt"}}
	result := &s3.ListObjectsV2Output{
		Prefix: aws.String("backend/docs/"),
		CommonPrefixes: []*s3.CommonPrefix{
			{Prefix: aws.String("backend/docs/folder ?#%&/")},
			{Prefix: aws.String("backend/docs/private/")},
			{Prefix: aws.String("elsewhere/folder/")},
			{Prefix: aws.String("backend/docs/../")},
		},
		Contents: []*s3.Object{
			{Key: aws.String("backend/docs/report ?#%&.txt")},
			{Key: aws.String("backend/docs/<script>alert(1)</script>.txt")},
			{Key: aws.String("backend/docs/<img onerror=alert(1)>.txt")},
			{Key: aws.String("backend/docs/secret.txt")},
			{Key: aws.String("backend/docs/")},
			{Key: aws.String("backend/docs/nested/file.txt")},
			{Key: aws.String("backend/docs/..")},
			{Key: aws.String("elsewhere/file.txt")},
		},
	}
	page := proxy.MakePageObj(result)
	if page.Count != 3 || len(page.Items) != 3 || !page.Items[0].IsDir {
		t.Fatalf("unexpected visible entries: %+v", page)
	}
	for _, item := range page.Items {
		link, err := url.Parse(item.Url)
		if err != nil || link.RawQuery != "" || link.Fragment != "" || link.Host != "" {
			t.Fatalf("unsafe link %q: %v", item.Url, err)
		}
		want := "./" + item.Name
		if item.IsDir {
			want += "/"
		}
		if link.Path != want {
			t.Fatalf("link targets %q, want %q", link.Path, want)
		}
	}
	response := httptest.NewRecorder()
	tpl := template.Must(template.New("listing").Parse(defaultBrowseTemplate))
	if err := page.GenerateHtml(response, tpl); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if strings.Contains(body, "<img") || strings.Contains(body, "<script") || !strings.Contains(body, "&lt;img") {
		t.Fatalf("object names were not escaped: %s", body)
	}
	empty := proxy.MakePageObj(&s3.ListObjectsV2Output{})
	response = httptest.NewRecorder()
	if err := empty.GenerateJson(response); err != nil || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("unexpected empty listing: %s, %v", response.Body.String(), err)
	}
}

func TestBrowseNavigation(t *testing.T) {
	tests := []struct {
		name, internal, original, path, parent string
		crumbs                                 int
	}{
		{name: "root", internal: "/", path: "/", crumbs: 1},
		{name: "nested", internal: "/one/two/", path: "/one/two/", parent: "../", crumbs: 3},
		{name: "stripped root", internal: "/", original: "/shares/", path: "/shares/", crumbs: 1},
		{name: "stripped nested", internal: "/one/", original: "/shares/one/", path: "/shares/one/", parent: "../", crumbs: 2},
		{name: "unrelated rewrite", internal: "/internal/", original: "/public/", path: "/public/", crumbs: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.internal, nil)
			if test.original != "" {
				original := *httptest.NewRequest(http.MethodGet, test.original, nil)
				request = request.WithContext(context.WithValue(request.Context(), caddyhttp.OriginalRequestCtxKey, original))
			}
			page := PageObj{}
			page.setNavigation(request)
			if page.Path != test.path || page.ParentLink != test.parent || len(page.Breadcrumbs) != test.crumbs {
				t.Fatalf("unexpected navigation: %+v", page)
			}
			if test.parent != "" && page.Breadcrumbs[0].Url != strings.Repeat("../", test.crumbs-1) {
				t.Fatalf("root breadcrumb escapes mount: %+v", page.Breadcrumbs)
			}
		})
	}
	page := PageObj{MoreLink: "?max=100&next=opaque%2Btoken", Items: []Item{{IsDir: true, Url: "./folder/"}, {Url: "./file.txt"}}}
	page.setNavigation(httptest.NewRequest(http.MethodGet, "/docs/?format=json&max=20&next=old-token", nil))
	if page.ParentLink != "../?format=json&max=20" || page.Items[0].Url != "./folder/?format=json&max=20" || page.Items[1].Url != "./file.txt" {
		t.Fatalf("incorrect query propagation: %+v", page)
	}
	if page.MoreLink != "?format=json&max=20&next=opaque%2Btoken" {
		t.Fatalf("incorrect continuation link: %s", page.MoreLink)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/?format=json", nil)
	original := *httptest.NewRequest(http.MethodGet, "/public/", nil)
	request = request.WithContext(context.WithValue(request.Context(), caddyhttp.OriginalRequestCtxKey, original))
	page = PageObj{MoreLink: "?next=token", Items: []Item{{IsDir: true, Url: "./folder/"}}}
	page.setNavigation(request)
	if page.ParentLink != "" || page.Items[0].Url != "./folder/?format=json" || page.MoreLink != "?format=json&next=token" {
		t.Fatalf("rewrite lost format selection: %+v", page)
	}
}

func TestBrowseHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("prefix") != "backend/docs/" || query.Get("delimiter") != "/" || query.Get("max-keys") != "20" || query.Get("continuation-token") != "old+token" {
			t.Errorf("incorrect S3 listing query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><KeyCount>3</KeyCount><IsTruncated>true</IsTruncated><NextContinuationToken>next+token</NextContinuationToken><CommonPrefixes><Prefix>backend/docs/folder/</Prefix></CommonPrefixes><Contents><Key>backend/docs/report.txt</Key><Size>1024</Size><LastModified>2026-01-01T00:00:00Z</LastModified></Contents><Contents><Key>backend/docs/secret.txt</Key><Size>0</Size></Contents></ListBucketResult>`))
	}))
	defer server.Close()
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"), Endpoint: aws.String(server.URL),
		S3ForcePathStyle: aws.Bool(true), Credentials: credentials.NewStaticCredentials("dummy", "dummy", ""),
	}))
	proxy := S3Proxy{
		Bucket: "bucket", Hide: []string{"secret.txt"}, EnableBrowse: true,
		client: s3.New(sess), log: zap.NewNop(),
		dirTemplate: template.Must(template.New("listing").Parse(defaultBrowseTemplate)),
	}
	for _, format := range []string{"html", "json"} {
		t.Run(format, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/docs/?format="+format+"&max=20&next=old%2Btoken", nil)
			response := httptest.NewRecorder()
			if err := proxy.BrowseHandler(response, request, "/backend/docs/"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(response.Body.String(), "secret.txt") {
				t.Fatal("hidden key leaked into listing")
			}
			if format == "json" {
				var page PageObj
				if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if page.Count != 2 || page.MoreLink != "?format=json&max=20&next=next%2Btoken" {
					t.Fatalf("incorrect page: %+v", page)
				}
			} else if strings.Contains(response.Body.String(), "backend/") || !strings.Contains(response.Body.String(), "Index of /docs/") {
				t.Fatalf("incorrect public path: %s", response.Body.String())
			}
			head := httptest.NewRecorder()
			if err := proxy.HeadHandler(head, request, "/backend/docs/"); err != nil {
				t.Fatal(err)
			}
			if head.Body.Len() != 0 || !reflect.DeepEqual(head.Header(), response.Header()) {
				t.Fatalf("HEAD differs from GET: %v vs %v", head.Header(), response.Header())
			}
			if response.Header().Get("Vary") != "Accept, Content-Type" || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("missing listing cache protections: %v", response.Header())
			}
		})
	}
	for _, query := range []string{"?format=xml", ""} {
		request := httptest.NewRequest(http.MethodGet, "/docs/"+query, nil)
		request.Header.Set("Accept", "image/png")
		if err := proxy.BrowseHandler(httptest.NewRecorder(), request, "/backend/docs/"); err == nil {
			t.Fatal("unsupported format did not fail before contacting S3")
		}
	}
}

func TestConstructListObjInput(t *testing.T) {
	type testCase struct {
		name        string
		key         string
		bucket      string
		queryString string
		expected    s3.ListObjectsV2Input
	}

	testCases := []testCase{
		testCase{
			name:   "no query options",
			bucket: "myBucket",
			key:    "/mypath/",
			expected: s3.ListObjectsV2Input{
				Bucket:    aws.String("myBucket"),
				Delimiter: aws.String("/"),
				Prefix:    aws.String("mypath/"),
				MaxKeys:   aws.Int64(defaultBrowsePageSize),
			},
		},
		testCase{
			name:        "max option",
			bucket:      "myBucket",
			key:         "/mypath/",
			queryString: "?max=20",
			expected: s3.ListObjectsV2Input{
				Bucket:    aws.String("myBucket"),
				Delimiter: aws.String("/"),
				Prefix:    aws.String("mypath/"),
				MaxKeys:   aws.Int64(20),
			},
		},
		testCase{
			name:        "max with next",
			bucket:      "myBucket",
			key:         "/mypath/",
			queryString: "?max=20&next=FOO",
			expected: s3.ListObjectsV2Input{
				Bucket:            aws.String("myBucket"),
				Delimiter:         aws.String("/"),
				Prefix:            aws.String("mypath/"),
				MaxKeys:           aws.Int64(20),
				ContinuationToken: aws.String("FOO"),
			},
		},
	}
	for _, tc := range testCases {
		r := http.Request{}
		u, _ := url.Parse(tc.queryString)
		r.URL = u
		p := S3Proxy{
			Bucket: tc.bucket,
		}
		result := p.ConstructListObjInput(&r, tc.key)
		if !reflect.DeepEqual(tc.expected, result) {
			t.Errorf("Expected obj %v, got %v.", tc.expected, result)
		}
	}
}

func TestMakePageObj(t *testing.T) {
	p := S3Proxy{}
	listOutput := s3.ListObjectsV2Output{
		KeyCount:              aws.Int64(20),
		NextContinuationToken: aws.String("next_token"),
		MaxKeys:               aws.Int64(20),
		CommonPrefixes: []*s3.CommonPrefix{
			&s3.CommonPrefix{
				Prefix: aws.String("/mydir"),
			},
			&s3.CommonPrefix{
				Prefix: aws.String("/otherdir"),
			},
		},
		Contents: []*s3.Object{
			&s3.Object{
				Key:          aws.String("/path/to/myobj"),
				Size:         aws.Int64(1024),
				LastModified: aws.Time(time.Date(1845, time.November, 10, 23, 0, 0, 0, time.UTC)),
			},
		},
	}

	result := p.MakePageObj(&listOutput)
	expected := PageObj{
		Count:    3,
		MoreLink: "?max=20&next=next_token",
		Items: []Item{
			Item{
				Url:   "./mydir/",
				IsDir: true,
				Name:  "mydir",
			},
			Item{
				Url:   "./otherdir/",
				IsDir: true,
				Name:  "otherdir",
			},
			Item{
				Url:          "./myobj",
				Key:          "/path/to/myobj",
				IsDir:        false,
				Name:         "myobj",
				Size:         "1.0 kB",
				LastModified: "a long while ago",
			},
		},
	}

	if !reflect.DeepEqual(expected, result) {
		t.Errorf("Expected obj %v, got %v.", expected, result)
	}
}
