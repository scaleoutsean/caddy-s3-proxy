[![golangci-lint Actions Status](https://github.com/scaleoutsean/caddy-s3-proxy/workflows/golangci-lint/badge.svg)](https://github.com/scaleoutsean/caddy-s3-proxy/actions)
[![Test Actions Status](https://github.com/scaleoutsean/caddy-s3-proxy/workflows/Test/badge.svg)](https://github.com/scaleoutsean/caddy-s3-proxy/actions)

# caddy-s3-proxy

caddy-s3-proxy allows you to proxy requests directly from S3.

S3 does have the website option, in which case, a normal reverse proxy could be used to display S3 data.
However, it is sometimes inconvenient to do that.  This module lets you access S3 data even if website access
is not configured on your bucket.

## Making a version of caddy with this plugin

With caddy 2 you can use [xcaddy](https://github.com/caddyserver/xcaddy) to build a version of caddy
with this plugin installed.  To install xcaddy do:
```
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
```

This repo has a Makefile to make it easier to build a new version of caddy with this plugin.  Just type:
```
make build
```

You can run ```make docker``` do build a local image you can test with.

## Publishing to GHCR

Pushing a version tag such as `v0.6.0` publishes the Docker image to `ghcr.io/scaleoutsean/caddy-s3-proxy`, tagged `0.6.0`, `0.6`, `0`, and `latest`. Make the GHCR package public once in its package settings to allow anonymous pulls, for example:

```sh
docker pull ghcr.io/scaleoutsean/caddy-s3-proxy:0.6.0
```

## Configuration
The Caddyfile directive would look something like this:
```
	s3proxy [<matcher>] {
		bucket <bucket_name>
		region <region_name>
                profile <aws profile>
		index  <list of index file names>
		endpoint <alternative S3 endpoint>
		root   <key prefix>
		enable_put
		enable_delete
 		force_path_style
		errors <http status> <S3 key to a custom error page for this http status>
		errors <S3 key to a default error page>
		browse [<path to template>]
	}
```

|  option   |  type  |  required | default | help |
|-----------|:------:|-----------|---------|------|
| bucket              | string   | yes |                          | S3 bucket name |
| region              | string   | no  |  us-east-1 | Region for signing S3 requests.|
| profile             | string   | no  |  empty string            | AWS profile if using shared credentials files. |
| endpoint            | string   | no  |  aws default             | S3 hostname |
| index               | string[] | no  |  [index.html, index.txt] | Index files to look up for dir path |
| root                | string   | no  |    | Set a "prefix" to be added to key |
| hide                | string[] | no  |    | Hide matching key paths from GET, HEAD, and directory listings. |
| enable_put          | bool     | no  | false   | Allow PUT method to be sent through proxy |
| enable_delete       | bool     | no  | false   | Allow DELETE method to be sent through proxy |
| force_path_style    | bool     | no  | false   | Set this to `true` to force S3 request to use path-style addressing |
| use_accelerate      | bool     | no  | false   | Set this to `true` to enable S3 Accelerate feature |
| errors              | [int, ] string | no |  | Custom error page or use "pass_through" to write nothing for errors. |
| browse              | [string] | no |  | Turns on a directory view for partial keys, an optional path to a template can be given |

Region precedence is: `region` in the Caddyfile, `AWS_REGION`, the selected AWS shared-config profile's region, then `us-east-1` if none of those are set.

## Directory Browsing

Directory browsing is **off by default**. Enable `browse` only on routes where
listing object names is intended. Requests whose paths end in `/` show a
directory listing when no configured index file is found; existing index files
and object downloads retain their normal behavior.

```caddyfile
s3proxy {
	bucket my-bucket
	root public
	browse
	hide private secret.txt
}
```

The default HTML view is script-free and includes breadcrumbs, parent navigation,
folders first, object sizes and modification times, and a next-page link.
Links support ordinary Tab/Enter keyboard navigation. It loads no external fonts,
scripts, or images. Backend root prefixes are not displayed in HTML; hidden entries,
directory-marker objects, and keys outside the requested prefix are excluded.

Listings request **100 entries per page** by default. Use `?max=20` to request a
different page size, up to S3's limit of 1,000; invalid values use the default.
Hidden entries can make a page shorter or empty. If there is a next-page link,
it remains available even on an empty page. Browsing never recursively lists the
bucket or fetches all pages, and the entry count describes visible entries on the
current page, not the bucket total.

Representation selection, in order:

- `?format=html` or `?format=json` explicitly selects a representation.
- Otherwise, `Accept` negotiates HTML or JSON, respecting quality weights and
  exclusions. Absent or wildcard preferences default to HTML; equally specific,
  equally weighted preferences also favor HTML. Unsupported preferences return 406.
- For compatibility, a request `Content-Type: application/json` selects JSON
  only when neither a format nor an `Accept` preference is supplied. New clients
  should use `Accept: application/json` or `?format=json` instead.

An unsupported nonempty `format` returns 400. Folder, breadcrumb, and next-page
links preserve explicit format and page-size choices; object links remain normal
download links. JSON retains the `count`, `items`, and `more` fields; empty
listings use `items: []`. Item `key` values remain full backend object keys, so
JSON access also exposes the configured root prefix. HEAD uses the same
representation and security headers without fetching a listing or sending a body.

Listing responses use `Cache-Control: private, no-store`, `Vary: Accept, Content-Type`,
and `X-Content-Type-Options: nosniff`. The built-in HTML template also applies a
Content Security Policy that blocks scripts, external resources, and framing.
An existing operator-supplied Content Security Policy is preserved.
An optional `browse /path/to/template.html` still uses a custom Go HTML template,
with `.Items`, `.Count`, `.MoreLink`, `.Path`, `.ParentLink`, and `.Breadcrumbs`;
the built-in template's CSP is not imposed on custom templates.

**Security:** browsing uses the proxy's S3 credentials, not the visitor's identity.
Protect the route with suitable authentication and authorization, and grant
`ListBucket` only for intended prefixes. `root` and `hide` are not substitutes for
backend IAM policy. The explorer has no mutation controls, but enabling `browse`
does not override `enable_put` or `enable_delete`. For a read-only deployment,
leave those options disabled and use read-only backend credentials. Directory
markers and dot-segment keys are omitted because they cannot be safely represented
as child navigation links. For stripped-prefix routes, breadcrumbs stay under the
visible mount; unrelated URL rewrites omit parent navigation.

## Credentials

This module uses the default providor chain to get credentials for access to S3.  This provides several more
secure options to provide credentials for accessing S3 without putting the credentials in the Caddyfile.
The methods include (and are looked for in this order):

1) Environment variables, i.e. `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`

2) Shared credentials file, by default located at `~/.aws/credentials`
   (You may pass the optional profile directive to select specific credentials.)

3) If your application uses an ECS task definition or RunTask API operation, IAM role for tasks.

4) If your application is running on an Amazon EC2 instance, IAM role for Amazon EC2.

For much more detail on the various options for setting AWS credentials see here:
https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/configuring-sdk.html

## Works with Versity S3 Gateway

The S3 proxy works with Versity S3 Gateway (VGW) for local testing. Set the endpoint directive to your VGW instance and enable `force_path_style`. See the [VGW Compose example](example/VGW_EXAMPLE.md) for a runnable setup.

## Handling errors

When accessing S3 you may get errors like keyNotFound, bucket does not exist, or ACL permissions problems.  By default, this proxy will map those errors to an http error - like 404, 403 or 500.

However, with the `errors` directive you have a couple of more options.  You can specify a S3 key that may contain HTML to display rather than just returning an error code.  This can be done for a specific error or all errors.  For example,

```sh
errors 403 /key/path/to/permissionerr.html
errors /key/path/to/defaulterr.html
```

This will display the page permissionerr.html for any 403 errors and defaulterr.html for all other errors.

There is a special option to "pass through" on an error and let the next Caddy handler deal with the request.  For example,

```sh
errors 404 pass_through
errors /key/path/to/defaulterr.html
```

Will pass 404 errors onto the next handler.  All other errors will show the page defaulterr.html.

Note: The `errors` direction only applies to GET method requests.  PUT and DELETE errors just return the code.

## Examples you can play with

In the example directory is an example of using the s3proxy with the [Versity S3 Gateway](https://github.com/versity/versitygw).

The compose contains a working version of S3 you can use for local development. Check out the example [here](example/VGW_EXAMPLE.md). You can also just run `make example` to build and start a docker image with the plugin and launch the compose example.

`example/Caddyfile-local` is a simple example for use with local CA-issued TLS certificate.

## Credits

- Original author: https://github.com/lindenlab/caddy-s3-proxy

This fork is maintained for personal use at [scaleoutsean/caddy-s3-proxy](https://github.com/scaleoutsean/caddy-s3-proxy).
