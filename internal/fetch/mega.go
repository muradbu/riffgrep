package fetch

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

// MEGA links carry the decryption key after the #: files are AES-128-CTR
// encrypted, and folder listings hold each file's key encrypted with the
// folder's. https://mega.nz/file/<h>#<key>, /folder/<h>#<key>, and the old
// #!<h>!<key> and #F!<h>!<key> forms.
var (
	megaNew = regexp.MustCompile(`^/(file|folder)/([\w-]+)$`)
	megaOld = regexp.MustCompile(`^(F?)!([\w-]+)!([\w-]+)`)
)

var megaSeq atomic.Int64

func (c *Client) listMega(ctx context.Context, u *url.URL) ([]*File, error) {
	var kind, handle, key string
	if m := megaNew.FindStringSubmatch(u.Path); m != nil {
		kind, handle = m[1], m[2]
		key, _, _ = strings.Cut(u.Fragment, "/")
	} else if m := megaOld.FindStringSubmatch(u.Fragment); m != nil {
		kind, handle, key = "file", m[2], m[3]
		if m[1] == "F" {
			kind = "folder"
		}
	} else {
		return nil, fmt.Errorf("%w: MEGA link without a file or folder key (%s)", ErrUnsupported, u.String())
	}
	k, err := megaB64(key)
	if err != nil {
		return nil, fmt.Errorf("MEGA link key: %w", err)
	}
	if kind == "folder" {
		return c.listMegaFolder(ctx, handle, k)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("MEGA file key has %d bytes, want 32", len(k))
	}
	dir, err := c.tempDir()
	if err != nil {
		return nil, err
	}
	p, err := c.downloadMega(ctx, map[string]any{"a": "g", "g": 1, "p": handle}, "", k, dir)
	if err != nil {
		return nil, err
	}
	return []*File{{Path: baseName(p), Size: fileSize(p), Local: p}}, nil
}

func megaB64(s string) ([]byte, error) {
	s = strings.NewReplacer("-", "+", "_", "/", ",", "").Replace(s)
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// megaAPI sends one command; folder is the shared folder's handle, for
// commands on its nodes.
func (c *Client) megaAPI(ctx context.Context, cmd map[string]any, folder string, out any) error {
	q := url.Values{"id": {fmt.Sprint(megaSeq.Add(1))}}
	if folder != "" {
		q.Set("n", folder)
	}
	body, _ := json.Marshal([]any{cmd})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://g.api.mega.co.nz/cs?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return err
	}
	// Errors are bare negative numbers, either alone or as the array's one
	// element.
	var code int
	if json.Unmarshal(b, &code) == nil || json.Unmarshal(b, &[]*int{&code}) == nil {
		if code < 0 {
			return fmt.Errorf("MEGA: %s", megaError(code))
		}
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil || len(arr) != 1 {
		return fmt.Errorf("MEGA: unexpected answer %.100s", b)
	}
	return json.Unmarshal(arr[0], out)
}

func megaError(code int) string {
	switch code {
	case -2:
		return "bad link"
	case -3, -4, -6:
		return "busy, try again later"
	case -9:
		return "file not found (removed?)"
	case -11, -16:
		return "access denied (the link was taken down)"
	case -17, -18:
		return "over quota or unavailable"
	}
	return fmt.Sprintf("error %d", code)
}

// fileKey turns a 32-byte MEGA file key into its AES key and CTR nonce.
func fileKey(k []byte) (key, iv []byte) {
	key = make([]byte, 16)
	for i := range key {
		key[i] = k[i] ^ k[i+16]
	}
	iv = make([]byte, 16)
	copy(iv, k[16:24])
	return key, iv
}

// megaAttrs decrypts a node's attributes: AES-CBC, zero IV, "MEGA{json}".
func megaAttrs(at string, key []byte) (name string, err error) {
	b, err := megaB64(at)
	if err != nil || len(b)%16 != 0 {
		return "", fmt.Errorf("bad attributes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(b, b)
	b = bytes.TrimRight(b, "\x00")
	if !bytes.HasPrefix(b, []byte("MEGA{")) {
		return "", fmt.Errorf("wrong key")
	}
	var a struct {
		N string `json:"n"`
	}
	if err := json.Unmarshal(b[4:], &a); err != nil {
		return "", err
	}
	return a.N, nil
}

func (c *Client) downloadMega(ctx context.Context, cmd map[string]any, folder string, k []byte, dir string) (string, error) {
	var r struct {
		G  string `json:"g"`
		At string `json:"at"`
		S  int64  `json:"s"`
	}
	if err := c.megaAPI(ctx, cmd, folder, &r); err != nil {
		return "", err
	}
	if r.G == "" {
		return "", fmt.Errorf("MEGA: no download URL (over the transfer quota?)")
	}
	key, iv := fileKey(k)
	name, err := megaAttrs(r.At, key)
	if err != nil {
		return "", fmt.Errorf("MEGA: %w", err)
	}
	resp, err := c.get(ctx, r.G)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, SafeName(name))
	f, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, cipher.StreamReader{S: cipher.NewCTR(block, iv), R: resp.Body})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && r.S > 0 && n != r.S {
		err = fmt.Errorf("MEGA: got %d of %d bytes", n, r.S)
	}
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	return dst, os.Rename(dst+".part", dst)
}

func (c *Client) listMegaFolder(ctx context.Context, handle string, folderKey []byte) ([]*File, error) {
	if len(folderKey) != 16 {
		return nil, fmt.Errorf("MEGA folder key has %d bytes, want 16", len(folderKey))
	}
	var r struct {
		F []struct {
			H string `json:"h"` // handle
			P string `json:"p"` // parent's handle
			T int    `json:"t"` // 0 file, 1 folder
			A string `json:"a"` // encrypted attributes
			K string `json:"k"` // "<owner>:<key encrypted with the folder key>"
			S int64  `json:"s"`
		} `json:"f"`
	}
	if err := c.megaAPI(ctx, map[string]any{"a": "f", "c": 1, "ca": 1, "r": 1}, handle, &r); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(folderKey)
	if err != nil {
		return nil, err
	}
	type node struct {
		name, parent string
		folder       bool
		key          []byte
		size         int64
	}
	nodes := map[string]*node{}
	var order []string
	for _, f := range r.F {
		_, enc, ok := strings.Cut(f.K, ":")
		if !ok {
			continue
		}
		k, err := megaB64(enc)
		if err != nil || len(k)%16 != 0 {
			continue
		}
		for i := 0; i < len(k); i += 16 {
			block.Decrypt(k[i:i+16], k[i:i+16])
		}
		attrKey := k
		if f.T == 0 {
			if len(k) != 32 {
				continue
			}
			attrKey, _ = fileKey(k)
		}
		name, err := megaAttrs(f.A, attrKey[:16])
		if err != nil {
			continue
		}
		nodes[f.H] = &node{name: name, parent: f.P, folder: f.T == 1, key: k, size: f.S}
		order = append(order, f.H)
	}
	pathOf := func(h string) string {
		var parts []string
		for n, depth := nodes[h], 0; n != nil && depth < 20; n, depth = nodes[n.parent], depth+1 {
			parts = append([]string{SafeName(n.name)}, parts...)
		}
		return strings.Join(parts, "/")
	}
	var files []*File
	for _, h := range order {
		n := nodes[h]
		if n.folder {
			continue
		}
		h, key := h, n.key
		files = append(files, &File{
			Path: pathOf(h),
			Size: n.size,
			fetch: func(ctx context.Context, dir string) (string, error) {
				return c.downloadMega(ctx, map[string]any{"a": "g", "g": 1, "n": h}, handle, key, dir)
			},
		})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("MEGA folder %s: no files (or the key doesn't fit)", handle)
	}
	return files, nil
}
