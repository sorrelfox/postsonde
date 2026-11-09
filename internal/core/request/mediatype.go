package request

import (
	"path"
	"strings"
)

// extensionMediaTypes maps lower-case file name extensions to the media
// type a file body or a multipart file part is sent with when no type is
// set (HTTP-14, HTTP-15).
//
// It is a fixed table, not mime.TypeByExtension: that one reads the
// system's MIME files, so the same request would be sent differently on
// different machines, and the editor could not show what is sent. The
// frontend has the same table (frontend/src/lib/contentType.ts); both are
// checked against the "extensions" of testdata/content_type.json.
var extensionMediaTypes = map[string]string{
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".css":  "text/css",
	".csv":  "text/csv",
	".gif":  "image/gif",
	".gz":   "application/gzip",
	".htm":  "text/html",
	".html": "text/html",
	".ico":  "image/vnd.microsoft.icon",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".js":   "text/javascript",
	".json": "application/json",
	".md":   "text/markdown",
	".mjs":  "text/javascript",
	".mp3":  "audio/mpeg",
	".mp4":  "video/mp4",
	".ogg":  "audio/ogg",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".tar":  "application/x-tar",
	".txt":  "text/plain",
	".wasm": "application/wasm",
	".wav":  "audio/wav",
	".webm": "video/webm",
	".webp": "image/webp",
	".xml":  "application/xml",
	".yaml": "application/yaml",
	".yml":  "application/yaml",
	".zip":  "application/zip",
}

// MediaTypeByExtension returns the media type of a file named name (a
// base name or a path with / or \ separators) by its extension, in any
// letter case, or "" for an extension the table does not have.
func MediaTypeByExtension(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return extensionMediaTypes[strings.ToLower(path.Ext(name))]
}

// FileMediaType returns the Content-Type of a file sent as a body or a
// multipart part: override when it is not empty, else the type of the
// file name's extension, else application/octet-stream.
func FileMediaType(name, override string) string {
	if override != "" {
		return override
	}
	if t := MediaTypeByExtension(name); t != "" {
		return t
	}
	return MediaTypeOctetStream
}
