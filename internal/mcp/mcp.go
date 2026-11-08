// Package mcp is the reverse channel: an MCP server (stdio) the agent can
// call. `open_in_editor` becomes an editor.open event that nvim handles.
// Register with: claude mcp add pekoe -- pekoe mcp
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"pekoe/internal/client"
)

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var tools = []map[string]any{
	{
		"name":        "open_in_editor",
		"description": "Open a file at a line in the user's Neovim and show a short note next to it. Use it to point the human at code you want them to look at.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "file path (absolute or relative to the worktree)"},
				"line": map[string]any{"type": "integer", "minimum": 1},
				"note": map[string]any{"type": "string", "description": "what to look at"},
			},
			"required": []string{"path", "line"},
		},
	},
	{
		"name":        "session_list",
		"description": "List pekoe sessions (worktrees with agents) and their states.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

func Serve(in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	slug := os.Getenv("PEKOE_SESSION")
	for sc.Scan() {
		var r rpc
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if len(r.ID) == 0 { // notification
			continue
		}
		rep := reply{JSONRPC: "2.0", ID: r.ID}
		switch r.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(r.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			rep.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "pekoe", "version": "0.1.0"},
			}
		case "ping":
			rep.Result = map[string]any{}
		case "tools/list":
			rep.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(r.Params, &p)
			text, isErr := call(p.Name, p.Arguments, slug)
			rep.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
		default:
			rep.Error = &rpcErr{Code: -32601, Message: "method not found: " + r.Method}
		}
		if err := enc.Encode(rep); err != nil {
			return err
		}
	}
	return sc.Err()
}

func call(name string, args json.RawMessage, slug string) (string, bool) {
	cl, err := client.Dial()
	if err != nil {
		return "pekoed is not running", true
	}
	defer cl.Close()
	switch name {
	case "open_in_editor":
		var a struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Note string `json:"note"`
		}
		json.Unmarshal(args, &a)
		if err := cl.Call("editor.open", map[string]any{"path": a.Path, "line": a.Line, "note": a.Note, "slug": slug}, nil); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("opened %s:%d in the editor", a.Path, a.Line), false
	case "session_list":
		var ss []map[string]any
		if err := cl.Call("session.list", nil, &ss); err != nil {
			return err.Error(), true
		}
		var b []byte
		for _, s := range ss {
			b = append(b, fmt.Sprintf("%v\t%v\t%v\t:%v\n", s["slug"], s["state"], s["branch"], s["port"])...)
		}
		if len(b) == 0 {
			return "no sessions", false
		}
		return string(b), false
	}
	return "unknown tool: " + name, true
}
