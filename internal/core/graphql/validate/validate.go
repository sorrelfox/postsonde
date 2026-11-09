// Package validate checks GraphQL executable documents against a schema,
// producing editor diagnostics with precise byte ranges (GQL-12).
//
// Validate runs a list of rules over a document from package parser in one
// walk. Every rule gets a Context holding the schema, the document, a
// TypeInfo that tracks the schema context of the node being visited, and a
// Report function. The rules follow the specification's section 5 and are
// named after graphql-js's rules.
//
// Documents are often half-typed, so rules never report on placeholder nodes
// (missing names, *ast.InvalidValue), which the parser already reported, and
// do not look into subtrees whose type is unknown: an unknown field is
// reported once, not together with every field below it.
//
// See docs/graphql-engine.md.
package validate

import (
	"cmp"
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Severity is how serious a diagnostic is.
type Severity string

// Severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Diagnostic codes.
const (
	// CodeUnknownField: a field the parent type does not have
	// (FieldsOnCorrectType). Range: the field name. Suggestions: similar
	// field names of the parent type.
	CodeUnknownField = "graphql.validate.unknown_field"
	// CodeUnknownArgument: an argument the field or directive does not
	// define (KnownArgumentNames). Range: the argument name. Suggestions:
	// similar argument names.
	CodeUnknownArgument = "graphql.validate.unknown_argument"
	// CodeMissingArgument: a required argument (non-null, no default) is
	// not given (ProvidedRequiredArguments). Range: the field or directive
	// name.
	CodeMissingArgument = "graphql.validate.missing_argument"
	// CodeInvalidValue: a literal that the expected input type does not
	// accept, such as a string for an Int, null for a non-null type or an
	// out-of-range Int (ValuesOfCorrectType). Range: the value. Suggestions:
	// enum values, for a string given where an enum is expected.
	CodeInvalidValue = "graphql.validate.invalid_value"
	// CodeUnknownEnumValue: an enum value the enum does not define
	// (ValuesOfCorrectType). Range: the value. Suggestions: similar values.
	CodeUnknownEnumValue = "graphql.validate.unknown_enum_value"
	// CodeUnknownInputField: an input object field the input type does not
	// define (ValuesOfCorrectType). Range: the field name. Suggestions:
	// similar input field names.
	CodeUnknownInputField = "graphql.validate.unknown_input_field"
	// CodeMissingInputField: a required input object field is not given
	// (ValuesOfCorrectType). Range: the object value.
	CodeMissingInputField = "graphql.validate.missing_input_field"
	// CodeOneOf: a @oneOf input object without exactly one field, or with
	// null for it (ValuesOfCorrectType), or a nullable variable given for
	// a @oneOf field (VariablesInAllowedPosition). Range: the object value,
	// the null value, or the variable.
	CodeOneOf = "graphql.validate.one_of"
	// CodeUnknownType: a type name the schema does not have, in a variable
	// definition or type condition (KnownTypeNames). Range: the type name.
	// Suggestions: similar type names.
	CodeUnknownType = "graphql.validate.unknown_type"
	// CodeSelectionOnLeaf: a selection set on a scalar or enum field
	// (ScalarLeafs). Range: the selection set.
	CodeSelectionOnLeaf = "graphql.validate.selection_on_leaf"
	// CodeMissingSelection: an object, interface or union field without a
	// selection set (ScalarLeafs). Range: the field name.
	CodeMissingSelection = "graphql.validate.missing_selection"

	// CodeDuplicateOperationName: an operation named like an earlier one
	// (UniqueOperationNames). Range: the later operation's name.
	CodeDuplicateOperationName = "graphql.validate.duplicate_operation_name"
	// CodeAnonymousOperation: an anonymous operation in a document with
	// other operations (LoneAnonymousOperation). Range: the operation
	// keyword, or the "{" of a shorthand query.
	CodeAnonymousOperation = "graphql.validate.anonymous_operation"
	// CodeUnknownOperationType: an operation whose root type the schema
	// does not have, typically a mutation or subscription
	// (KnownOperationTypes). Range: the operation keyword, or the "{" of a
	// shorthand query.
	CodeUnknownOperationType = "graphql.validate.unknown_operation_type"
	// CodeSingleFieldSubscription: a subscription with more than one root
	// field, or an introspection root field (SingleFieldSubscriptions).
	// Range: the extra or introspection field's name.
	CodeSingleFieldSubscription = "graphql.validate.single_field_subscription"
	// CodeUnknownFragment: a spread of a fragment the document does not
	// define (KnownFragmentNames). Range: the spread's name. Suggestions:
	// similar fragment names.
	CodeUnknownFragment = "graphql.validate.unknown_fragment"
	// CodeDuplicateFragmentName: a fragment named like an earlier one
	// (UniqueFragmentNames). Range: the later fragment's name.
	CodeDuplicateFragmentName = "graphql.validate.duplicate_fragment_name"
	// CodeFragmentOnNonComposite: a type condition on a scalar, enum or
	// input object (FragmentsOnCompositeTypes). Range: the type name.
	CodeFragmentOnNonComposite = "graphql.validate.fragment_on_non_composite"
	// CodeImpossibleFragmentSpread: a fragment whose type condition can
	// never apply to the parent type (PossibleFragmentSpreads). Range: the
	// inline fragment's type name, or the spread's name.
	CodeImpossibleFragmentSpread = "graphql.validate.impossible_fragment_spread"
	// CodeUnusedFragment: a fragment no operation uses (NoUnusedFragments).
	// Range: the fragment name.
	CodeUnusedFragment = "graphql.validate.unused_fragment"
	// CodeFragmentCycle: a fragment that spreads itself, directly or
	// through others (NoFragmentCycles). Range: the name of the spread that
	// closes the cycle.
	CodeFragmentCycle = "graphql.validate.fragment_cycle"
	// CodeDuplicateVariable: a variable defined twice (UniqueVariableNames).
	// Range: the later definition's variable.
	CodeDuplicateVariable = "graphql.validate.duplicate_variable"
	// CodeNonInputVariableType: a variable of an object, interface or union
	// type (VariablesAreInputTypes). Range: the type.
	CodeNonInputVariableType = "graphql.validate.non_input_variable_type"
	// CodeUndefinedVariable: a variable the operation does not define
	// (NoUndefinedVariables). Range: the usage, "$" included.
	CodeUndefinedVariable = "graphql.validate.undefined_variable"
	// CodeUnusedVariable: a variable the operation defines but does not use
	// (NoUnusedVariables). Range: the variable in its definition, "$"
	// included.
	CodeUnusedVariable = "graphql.validate.unused_variable"
	// CodeVariablePosition: a variable whose type does not fit where it is
	// used (VariablesInAllowedPosition). Range: the usage. A nullable
	// variable for a @oneOf field is CodeOneOf.
	CodeVariablePosition = "graphql.validate.variable_position"
	// CodeUnknownDirective: a directive the schema does not define
	// (KnownDirectives). Range: the directive name. Suggestions: similar
	// directive names, without "@".
	CodeUnknownDirective = "graphql.validate.unknown_directive"
	// CodeMisplacedDirective: a directive used where its definition does
	// not allow it (KnownDirectives). Range: the directive name.
	CodeMisplacedDirective = "graphql.validate.misplaced_directive"
	// CodeDuplicateDirective: a non-repeatable directive used twice on the
	// same node (UniqueDirectivesPerLocation). Range: the later directive's
	// name.
	CodeDuplicateDirective = "graphql.validate.duplicate_directive"
	// CodeDuplicateArgument: an argument given twice (UniqueArgumentNames).
	// Range: the later argument's name.
	CodeDuplicateArgument = "graphql.validate.duplicate_argument"
	// CodeDuplicateInputField: an input object field given twice in one
	// object value (UniqueInputFieldNames). Range: the later field's name.
	CodeDuplicateInputField = "graphql.validate.duplicate_input_field"
)

// Diagnostic is one problem in a document.
type Diagnostic struct {
	// Code is one of the Code constants: stable, for quick-fixes and tests.
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	// Start and End are the byte range [Start, End) of the offending node:
	// the name of an unknown field or argument, a wrong value. Convert them
	// for the editor with lexer.UTF16Index.
	Start int `json:"start"`
	End   int `json:"end"`
	// Message follows graphql-js's wording, suggestions included.
	Message string `json:"message"`
	// Suggestions are replacements for the text in [Start, End), best first,
	// at most MaxSuggestions. Which codes have them is documented with the
	// codes.
	Suggestions []string `json:"suggestions,omitempty"`
}

// Visitor is what a rule does while the document is walked. Enter is called
// before a node's children and Leave after them; both may be nil. When Enter
// returns false, the rule skips the node's children and its Leave; other
// rules still visit them.
type Visitor struct {
	Enter func(n ast.Node) bool
	Leave func(n ast.Node)
}

// Rule is a validation rule. New is called once per validation and returns
// the rule's visitor, which reads the context through c.
type Rule struct {
	// Name is the graphql-js name of the rule, such as
	// "FieldsOnCorrectType".
	Name string
	New  func(c *Context) Visitor
}

// Context is the state shared by the rules of one validation.
type Context struct {
	Schema   *schema.Schema
	Doc      *ast.Document
	TypeInfo *TypeInfo

	fragments map[string]*ast.FragmentDefinition
	info      documentInfo
	diags     []Diagnostic
}

// Report adds a diagnostic. A zero Severity becomes SeverityError.
func (c *Context) Report(d Diagnostic) {
	if d.Severity == "" {
		d.Severity = SeverityError
	}
	c.diags = append(c.diags, d)
}

// report adds an error on the range of n.
func (c *Context) report(code string, n ast.Node, message string, suggestions []string) {
	l := n.Pos()
	c.Report(Diagnostic{Code: code, Start: l.Start, End: l.End, Message: message, Suggestions: suggestions})
}

// Fragment returns the first fragment definition named name, or nil.
func (c *Context) Fragment(name string) *ast.FragmentDefinition {
	if c.fragments == nil {
		c.fragments = map[string]*ast.FragmentDefinition{}
		for _, d := range c.Doc.Definitions {
			if f, ok := d.(*ast.FragmentDefinition); ok && f.Name != nil && !f.Name.Missing() {
				if _, dup := c.fragments[f.Name.Value]; !dup {
					c.fragments[f.Name.Value] = f
				}
			}
		}
	}
	return c.fragments[name]
}

// DefaultRules returns the rules Validate runs, in a new slice.
func DefaultRules() []Rule {
	return []Rule{
		FieldsOnCorrectType,
		KnownArgumentNames,
		ProvidedRequiredArguments,
		ValuesOfCorrectType,
		KnownTypeNames,
		ScalarLeafs,
		UniqueOperationNames,
		LoneAnonymousOperation,
		KnownOperationTypes,
		SingleFieldSubscriptions,
		FragmentsOnCompositeTypes,
		VariablesAreInputTypes,
		UniqueFragmentNames,
		KnownFragmentNames,
		NoUnusedFragments,
		PossibleFragmentSpreads,
		NoFragmentCycles,
		UniqueVariableNames,
		NoUndefinedVariables,
		NoUnusedVariables,
		KnownDirectives,
		UniqueDirectivesPerLocation,
		UniqueArgumentNames,
		VariablesInAllowedPosition,
		UniqueInputFieldNames,
	}
}

// Validate checks doc against s with DefaultRules. Diagnostics are sorted by
// Start. It returns nil when s or doc is nil.
func Validate(s *schema.Schema, doc *ast.Document) []Diagnostic {
	return ValidateRules(s, doc, DefaultRules())
}

// ValidateRules checks doc against s with rules, in one walk. Diagnostics
// are sorted by Start, and by rule order for the same Start.
func ValidateRules(s *schema.Schema, doc *ast.Document, rules []Rule) []Diagnostic {
	if s == nil || doc == nil {
		return nil
	}
	c := &Context{Schema: s, Doc: doc, TypeInfo: NewTypeInfo(s)}
	vs := make([]Visitor, len(rules))
	for i, r := range rules {
		vs[i] = r.New(c)
	}
	Walk(doc, c.TypeInfo, vs...)
	slices.SortStableFunc(c.diags, func(a, b Diagnostic) int { return cmp.Compare(a.Start, b.Start) })
	return c.diags
}

// Walk visits the tree rooted at root depth-first in source order, keeping
// ti in step (ti.Enter before the visitors' Enter, ti.Leave after their
// Leave), with the visitors in parallel: a visitor whose Enter returns false
// skips that subtree while the others continue. ti may be nil.
func Walk(root ast.Node, ti *TypeInfo, visitors ...Visitor) {
	w := walker{ti: ti, vs: visitors, skip: make([]ast.Node, len(visitors))}
	w.walk(root)
}

type walker struct {
	ti   *TypeInfo
	vs   []Visitor
	skip []ast.Node // the node a visitor is skipping, or nil
}

func (w *walker) walk(n ast.Node) {
	if w.ti != nil {
		w.ti.Enter(n)
	}
	for i, v := range w.vs {
		if w.skip[i] == nil && v.Enter != nil && !v.Enter(n) {
			w.skip[i] = n
		}
	}
	for _, c := range ast.Children(n) {
		w.walk(c)
	}
	for i, v := range w.vs {
		switch w.skip[i] {
		case nil:
			if v.Leave != nil {
				v.Leave(n)
			}
		case n:
			w.skip[i] = nil
		}
	}
	if w.ti != nil {
		w.ti.Leave(n)
	}
}
