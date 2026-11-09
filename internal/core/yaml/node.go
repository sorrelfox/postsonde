package yaml

// Kind is the kind of a Node.
type Kind uint8

const (
	// ScalarNode is a scalar: Value holds its text, Tag its type.
	ScalarNode Kind = iota + 1
	// MappingNode is a mapping: Pairs in document order.
	MappingNode
	// SequenceNode is a sequence: Items in document order.
	SequenceNode
	// AliasNode is an alias (*name): Value is the anchor name and Target
	// the node it refers to.
	AliasNode
)

// String returns "scalar", "mapping", "sequence" or "alias".
func (k Kind) String() string {
	switch k {
	case ScalarNode:
		return "scalar"
	case MappingNode:
		return "mapping"
	case SequenceNode:
		return "sequence"
	case AliasNode:
		return "alias"
	}
	return "unknown"
}

// Style is how a node was written.
type Style uint8

const (
	// Plain is an unquoted scalar, or a block collection.
	Plain Style = iota
	// SingleQuoted is a 'scalar'.
	SingleQuoted
	// DoubleQuoted is a "scalar".
	DoubleQuoted
	// Literal is a | block scalar.
	Literal
	// Folded is a > block scalar.
	Folded
	// Flow is a [sequence] or {mapping}.
	Flow
)

// Tags of resolved nodes, in Node.Tag.
const (
	TagNull  = "!!null"
	TagBool  = "!!bool"
	TagInt   = "!!int"
	TagFloat = "!!float"
	TagStr   = "!!str"
	TagMap   = "!!map"
	TagSeq   = "!!seq"
)

// Node is one node of a YAML document.
type Node struct {
	Kind  Kind
	Style Style
	// Tag is the resolved type: TagNull, TagBool, TagInt, TagFloat or
	// TagStr for scalars, TagMap and TagSeq for collections, "" for
	// aliases. Plain scalars are typed by the core schema, quoted and
	// block scalars are strings, unless an explicit tag says otherwise.
	Tag string
	// Value is a scalar's text with escapes and line folding applied, or
	// an alias's anchor name.
	Value string
	// Anchor is the node's anchor name (&name), if any.
	Anchor string
	// Pairs are a mapping's entries in document order.
	Pairs []Pair
	// Items are a sequence's entries in document order.
	Items []*Node
	// Target is the node an alias refers to.
	Target *Node
	// Line and Column (1-based, in characters) are where the node's
	// content starts.
	Line, Column int
}

// Pair is a mapping entry.
type Pair struct {
	Key, Value *Node
}

// Resolve returns the node an alias refers to (following aliases), or n
// itself.
func (n *Node) Resolve() *Node {
	for n != nil && n.Kind == AliasNode {
		n = n.Target
	}
	return n
}

// Get returns the value of the first entry of a mapping whose key is a
// scalar with the text key, or nil. Aliases are resolved on both sides.
// Merge keys (<<) are not applied; use JSON for the merged view.
func (n *Node) Get(key string) *Node {
	n = n.Resolve()
	if n == nil || n.Kind != MappingNode {
		return nil
	}
	for _, p := range n.Pairs {
		if k := p.Key.Resolve(); k.Kind == ScalarNode && k.Value == key {
			return p.Value
		}
	}
	return nil
}

// Limits bound the work Parse and JSON do. A zero field means the
// DefaultLimits value.
type Limits struct {
	// MaxBytes is the largest input accepted.
	MaxBytes int
	// MaxDepth is the deepest nesting of collections.
	MaxDepth int
	// MaxNodes bounds the nodes in the parsed tree, and the nodes JSON
	// writes after alias expansion.
	MaxNodes int
}

// DefaultLimits are used for zero Limits fields: 32 MiB, 1000 levels,
// 2,000,000 nodes.
var DefaultLimits = Limits{MaxBytes: 32 << 20, MaxDepth: 1000, MaxNodes: 2_000_000}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits.MaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = DefaultLimits.MaxNodes
	}
	return l
}
