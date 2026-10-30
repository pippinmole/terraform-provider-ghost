package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// contentHeadSize is how many leading bytes inspect keeps, enough to sniff
// a file type.
const contentHeadSize = 512

// contentSource is where an uploaded file's content comes from: exactly one
// of a local file, a UTF-8 string or a base64 string. A resource without a
// string argument leaves Content null.
type contentSource struct {
	File, Content, ContentBase64 types.String
	// noun names what is uploaded in error messages, e.g. "asset".
	noun string
}

// contentInfo describes content without holding all of it.
type contentInfo struct {
	sha256 string
	size   int64
	// head holds the first contentHeadSize bytes.
	head []byte
}

// same reports whether s and o take their content from the same file,
// string or base64 string.
func (s contentSource) same(o contentSource) bool {
	return s.File.Equal(o.File) && s.Content.Equal(o.Content) && s.ContentBase64.Equal(o.ContentBase64)
}

// attr returns the attribute the content comes from.
func (s contentSource) attr() path.Path {
	switch {
	case !s.Content.IsNull():
		return path.Root("content")
	case !s.ContentBase64.IsNull():
		return path.Root("content_base64")
	default:
		return path.Root("file")
	}
}

// inspect hashes the content. known is false when the content isn't known
// yet: a value that is unknown until apply, or a file that doesn't exist
// yet. Empty content is an error.
func (s contentSource) inspect() (info contentInfo, known bool, diags diag.Diagnostics) {
	switch {
	case s.File.IsUnknown() || s.Content.IsUnknown() || s.ContentBase64.IsUnknown():
		return contentInfo{}, false, nil
	case !s.Content.IsNull():
		info = inspectBytes([]byte(s.Content.ValueString()))
	case !s.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(s.ContentBase64.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("content_base64"), "Invalid base64", err.Error())
			return contentInfo{}, false, diags
		}
		info = inspectBytes(b)
	case !s.File.IsNull():
		var err error
		info, err = inspectFile(s.File.ValueString())
		if errors.Is(err, fs.ErrNotExist) {
			return contentInfo{}, false, nil
		}
		if err != nil {
			diags.AddAttributeError(path.Root("file"), "Can't read file", err.Error())
			return contentInfo{}, false, diags
		}
	default:
		// The resource's ExactlyOneOf reports it.
		return contentInfo{}, false, nil
	}

	if info.size == 0 {
		diags.Append(s.emptyError())
		return contentInfo{}, false, diags
	}

	return info, true, nil
}

func (s contentSource) emptyError() diag.Diagnostic {
	return diag.NewAttributeErrorDiagnostic(s.attr(), "Empty "+s.noun,
		fmt.Sprintf("Ghost doesn't accept empty files: the %s's content must be at least 1 byte.", s.noun))
}

// open opens the content and hashes it.
func (s contentSource) open() (*assetContent, diag.Diagnostics) {
	var diags diag.Diagnostics
	attr := s.attr()

	switch {
	case !s.Content.IsNull():
		b := []byte(s.Content.ValueString())
		return &assetContent{reader: bytes.NewReader(b), contentInfo: inspectBytes(b), attr: attr}, nil
	case !s.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(s.ContentBase64.ValueString())
		if err != nil {
			diags.AddAttributeError(attr, "Invalid base64", err.Error())
			return nil, diags
		}
		return &assetContent{reader: bytes.NewReader(b), contentInfo: inspectBytes(b), attr: attr}, nil
	}

	name := s.File.ValueString()
	info, err := inspectFile(name)
	if err != nil {
		diags.AddAttributeError(attr, "Can't read file", err.Error())
		return nil, diags
	}
	f, err := os.Open(name)
	if err != nil {
		diags.AddAttributeError(attr, "Can't read file", err.Error())
		return nil, diags
	}

	return &assetContent{reader: f, contentInfo: info, attr: attr, close: f.Close}, nil
}

func inspectBytes(b []byte) contentInfo {
	h := sha256.Sum256(b)
	head := b[:min(len(b), contentHeadSize)]

	return contentInfo{sha256: hex.EncodeToString(h[:]), size: int64(len(b)), head: bytes.Clone(head)}
}

func inspectFile(name string) (contentInfo, error) {
	f, err := os.Open(name)
	if err != nil {
		return contentInfo{}, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	head := &limitedBuffer{limit: contentHeadSize}
	n, err := io.Copy(io.MultiWriter(h, head), f)
	if err != nil {
		return contentInfo{}, fmt.Errorf("reading %s: %w", name, err)
	}

	return contentInfo{sha256: hex.EncodeToString(h.Sum(nil)), size: n, head: head.Bytes()}, nil
}

// limitedBuffer keeps the first limit bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}

	return len(p), nil
}

// assetContent is content to upload.
type assetContent struct {
	contentInfo
	reader io.ReaderAt
	attr   path.Path
	close  func() error
}

func (c *assetContent) Close() error {
	if c.close == nil {
		return nil
	}

	return c.close()
}
