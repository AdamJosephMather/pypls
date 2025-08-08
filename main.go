package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"unicode"

	"github.com/sourcegraph/jsonrpc2"
)

type CompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind"`
	InsertText    string `json:"insertText"`
	InsertTextFmt int    `json:"insertTextFormat,omitempty"`
	SortText      string `json:"sortText"`
}

type OpenFile struct {
	uri string
	content string
	words map[string]int64
}

var files map[string]OpenFile
var defaultCompletions map[string]int64

type LogMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

func log(ctx context.Context, conn *jsonrpc2.Conn, message string) {
	conn.Notify(ctx, "window/logMessage", LogMessageParams{
		Type:    4,
		Message: message,
	})
}

type handler struct{}

func getURI(req *jsonrpc2.Request) (string, error) {
	var payload struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	
	// unmarshal the raw params into it
	if err := json.Unmarshal(*req.Params, &payload); err != nil {
		return "", errors.New("Failed to unmarshal payload")
	}
	
	uri := payload.TextDocument.URI
	return uri, nil
}

func getWords(text *string) map[string]int64 {
	words := make(map[string]int64)
	currentword := ""
	
	for _, c := range *text {
		if c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c) {
			currentword += string(c)
		}else{
			if currentword != "" && defaultCompletions[currentword] == 0 { // let's not promote builtins because there *will* be more of those and we can all agree variables are *probably* more important
				words[currentword] = words[currentword] + 1 // words[currentword] may evaluate to 0, but then we can add one and assign (not the same as += because of non initialized keys)
			}
			currentword = ""
		}
	}
	
	if currentword != "" {
		words[currentword] = words[currentword] + 1 // words[currentword] may evaluate to 0, but then we can add one and assign (not the same as += because of non initialized keys)
	}
	
	return words
}

func padStart(s string, pad string, length int) string {
	for len(s) < length {
		s = pad+s;
	}
	return s
}

func (h *handler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	switch req.Method {
	case "initialize":
		var result struct {
			Capabilities struct {
				CompletionProvider struct {
					TriggerCharacters []string `json:"triggerCharacters"`
				} `json:"completionProvider"`
			} `json:"capabilities"`
		}
		
		result.Capabilities.CompletionProvider.TriggerCharacters = []string{".",":"}
		conn.Reply(ctx, req.ID, result)
	
	case "initialized":
		log(ctx, conn, "Language server initialized successfully")

	case "shutdown":
		conn.Reply(ctx, req.ID, nil)

	case "exit":
		os.Exit(0)
	
	case "workspace/didChangeConfiguration":
		log(ctx, conn, "Ack")
	
	case "textDocument/didChange":
		uri, err := getURI(req)
		
		if err != nil {
			log(ctx, conn, err.Error())
			return
		}
		
		var params struct {
			ContentChanges []struct{ Text string `json:"text"` } `json:"contentChanges"`
		}
		
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeParseError,
				Message: "invalid change params: " + err.Error(),
			})
			return
		}
		
		files[uri] = OpenFile{ uri, params.ContentChanges[0].Text, getWords(&params.ContentChanges[0].Text) }
		
	case "textDocument/didOpen": // get uri from params
		uri, err := getURI(req)
		
		if err != nil {
			log(ctx, conn, err.Error())
			return
		}
		
		var params struct {
			TextDocument struct{ Text string `json:"text"` } `json:"textDocument"`
		}
		
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeParseError,
				Message: "invalid open params: " + err.Error(),
			})
			return
		}
		
		files[uri] = OpenFile{ uri, params.TextDocument.Text, getWords(&params.TextDocument.Text) }
	
	case "textDocument/didSave":
		
	case "textDocument/hover":
		
	case "textDocument/completion":
		uri, err := getURI(req)
		
		if err != nil {
			log(ctx, conn, err.Error())
			return
		}
		
		file, ok := files[uri]
		
		if !ok {
			log(ctx, conn, "FILE NOT OPEN")
			return
		}
		
		filecontent := file.content
		
		var params struct {
			Position     struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"position"`
		}
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeParseError,
				Message: "invalid completion params: " + err.Error(),
			})
			return
		}
		
		curline := 0
		line_pos := 0
		curword := ""
		tocomplete := ""
		
		leadup := make([]string, 0)
		
		for _, c := range filecontent {
			line_pos ++
			
			if c == '\n' {
				if curline == params.Position.Line && (line_pos == params.Position.Character) {
					tocomplete = curword
				}
				
				curline ++
				line_pos = 0
				if curline > params.Position.Line {
					break
				}
			}else if curline == params.Position.Line {
				if c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c) {
					curword += string(c)
				}else if (c == '.') {
					leadup = append(leadup, curword)
					curword = "";
				}else{
					leadup = make([]string, 0);
					curword = ""
				}
				
				if (line_pos == params.Position.Character) {
					tocomplete = curword;
				}
			}
		}
		
		if (line_pos+1 == params.Position.Character && curline == params.Position.Line) {
			tocomplete = curword
		}
		
		leadups := ""
		for _, li := range leadup {
			leadups += li+"."
		}
		
		log(ctx, conn, leadups+tocomplete)
		
		
		items := make([]CompletionItem, 0)
		
		padLen := 6;
		for key, value := range file.words {
			if key == tocomplete { continue }
			items = append(items, CompletionItem{ key, 3, key, 1, padStart(strconv.FormatInt(1000000-value, 10), "0", padLen), } )
		}
		for key, value := range defaultCompletions {
			if key == tocomplete { continue }
			items = append(items, CompletionItem{ key, 3, key, 1, padStart(strconv.FormatInt(1000000-value, 10), "0", padLen), } )
		}
		
		var resp struct {
			IsIncomplete bool        `json:"isIncomplete"`
			Items        interface{} `json:"items"`
		}
		resp.IsIncomplete = false
		resp.Items = items
	
		conn.Reply(ctx, req.ID, resp)

	default:
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "method not found < " + req.Method,
		})
	}
}

func main() {
	defaultCompletions = make(map[string]int64)
	
	defs := []string{"for", "range", "import", "int", "if", "elif", "else", "in", "open", "sort", "sorted", "def", "print", "continue", "break", "return", "not", "del", "eval", "True", "False", "str", "while", "and", "as", "is", "or", "try", "except", "finally", "raise", "assert", "with", "lambda", "yield", "async", "await", "class", "from", "global", "nonlocal", "pass", "None", "abs", "all", "any", "ascii", "bin", "bool", "breakpoint", "bytearray", "bytes", "callable", "chr", "classmethod", "compile", "complex", "delattr", "dict", "dir", "divmod", "enumerate", "exec", "filter", "float", "format", "frozenset", "getattr", "globals", "hasattr", "hash", "help", "hex", "id", "input", "isinstance", "issubclass", "iter", "len", "list", "locals", "map", "max", "memoryview", "min", "next", "object", "oct", "pow", "property", "repr", "reversed", "round", "set", "setattr", "slice", "staticmethod", "sum", "super", "tuple", "type", "vars", "zip", "__import__"}
	
	for _, d := range defs {
		defaultCompletions[d] = 11
	}
	
	files = make(map[string]OpenFile)
	
	ctx := context.Background()
	
	stream := jsonrpc2.NewBufferedStream(
		struct {
			io.Reader
			io.Writer
			io.Closer
		}{os.Stdin, os.Stdout, io.NopCloser(nil)},
		jsonrpc2.VSCodeObjectCodec{},
	)
	conn := jsonrpc2.NewConn(ctx, stream, &handler{})
	<-conn.DisconnectNotify()
}
