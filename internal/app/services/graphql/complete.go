package graphql

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/graphql/complete"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// MaxCompletionItems bounds the items Complete returns; Count says how many
// matched. The editor asks again on every keystroke, so the best ones are
// enough.
const MaxCompletionItems = 300

// Complete returns the suggestions at in.Offset (UTF-16 code units, a
// CodeMirror position) of the GraphQL document in.Text, from the cached
// schema in.Key (SchemaInfo's Key; "" or an unknown key completes syntax
// only: keywords, built-in scalars and directives) (GQL-13). It works
// offline: the parsed schema comes from the service's memory, or from the
// cache on disk the first time, and is never re-parsed per call.
func (s *GraphQLService) Complete(ctx context.Context, in CompleteInputDTO) (CompletionDTO, error) {
	if err := interrupted(ctx, "Completing the GraphQL document"); err != nil {
		return CompletionDTO{}, err
	}
	var sch *schema.Schema
	if in.Key != "" {
		if e := s.cached(in.Key); e != nil {
			sch = e.Schema
		}
	}
	return completion(sch, in.Text, in.Offset), nil
}

func completion(sch *schema.Schema, text string, offset16 int) CompletionDTO {
	idx := lexer.NewUTF16Index(text)
	res := complete.Complete(sch, text, idx.ToByte(max(offset16, 0)))
	out := CompletionDTO{
		Context:      string(res.Context),
		From:         idx.ToUTF16(res.From),
		To:           idx.ToUTF16(res.To),
		Prefix:       res.Prefix,
		ParentType:   res.ParentType,
		Owner:        res.Owner,
		ExpectedType: res.ExpectedType,
		Schema:       sch != nil,
		Count:        len(res.Items),
		Items:        make([]CompletionItemDTO, 0, min(len(res.Items), MaxCompletionItems)),
	}
	for i, it := range res.Items {
		if i == MaxCompletionItems {
			break
		}
		out.Items = append(out.Items, CompletionItemDTO{
			Label:             it.Label,
			Kind:              string(it.Kind),
			Detail:            it.Detail,
			Documentation:     it.Documentation,
			Deprecated:        it.Deprecated,
			DeprecationReason: it.DeprecationReason,
			InsertText:        it.InsertText,
			Suffix:            it.Suffix,
			// Suffixes are ASCII: bytes are UTF-16 code units.
			SuffixCursor: it.SuffixCursor,
		})
	}
	return out
}
