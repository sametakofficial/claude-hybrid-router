package anthropic

import "strings"

// SignaturePrefix marks thinking blocks that claude-hybrid made from a
// provider's reasoning text (OpenAI-style reasoning_content / reasoning).
// They carry no real Anthropic signature, so they must never be sent to
// Anthropic (it would reject them). The rest of the signature names the
// field the reasoning came in, so it can be sent back the same way.
const SignaturePrefix = "claude-hybrid:"

// IsPlaceholderSignature reports a signature made by claude-hybrid.
func IsPlaceholderSignature(sig string) bool { return strings.HasPrefix(sig, SignaturePrefix) }

// DropPlaceholderThinking removes thinking blocks with a placeholder
// signature from the messages; it reports whether anything was removed.
func DropPlaceholderThinking(r Request) bool {
	msgs, _ := r["messages"].([]any)
	changed := false
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		kept := blocks[:0]
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "thinking" {
				if sig, _ := bm["signature"].(string); IsPlaceholderSignature(sig) {
					changed = true
					continue
				}
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			kept = append(kept, map[string]any{"type": "text", "text": "(reasoning omitted)"})
		}
		msg["content"] = kept
	}
	return changed
}
