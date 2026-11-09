package desc

import "strconv"

// Kind is a field's type. The numbers match
// google.protobuf.FieldDescriptorProto.Type.
type Kind uint8

// Kinds.
const (
	KindUnknown  Kind = 0 // the type did not resolve
	KindDouble   Kind = 1
	KindFloat    Kind = 2
	KindInt64    Kind = 3
	KindUint64   Kind = 4
	KindInt32    Kind = 5
	KindFixed64  Kind = 6
	KindFixed32  Kind = 7
	KindBool     Kind = 8
	KindString   Kind = 9
	KindGroup    Kind = 10
	KindMessage  Kind = 11
	KindBytes    Kind = 12
	KindUint32   Kind = 13
	KindEnum     Kind = 14
	KindSfixed32 Kind = 15
	KindSfixed64 Kind = 16
	KindSint32   Kind = 17
	KindSint64   Kind = 18
)

var kindNames = [...]string{
	KindUnknown: "unknown", KindDouble: "double", KindFloat: "float", KindInt64: "int64",
	KindUint64: "uint64", KindInt32: "int32", KindFixed64: "fixed64", KindFixed32: "fixed32",
	KindBool: "bool", KindString: "string", KindGroup: "group", KindMessage: "message",
	KindBytes: "bytes", KindUint32: "uint32", KindEnum: "enum", KindSfixed32: "sfixed32",
	KindSfixed64: "sfixed64", KindSint32: "sint32", KindSint64: "sint64",
}

// String returns the .proto keyword for scalars, and "message", "group",
// "enum" or "unknown" otherwise.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

var scalarKinds = map[string]Kind{
	"double": KindDouble, "float": KindFloat, "int64": KindInt64, "uint64": KindUint64,
	"int32": KindInt32, "fixed64": KindFixed64, "fixed32": KindFixed32, "bool": KindBool,
	"string": KindString, "bytes": KindBytes, "uint32": KindUint32, "sfixed32": KindSfixed32,
	"sfixed64": KindSfixed64, "sint32": KindSint32, "sint64": KindSint64,
}

// ScalarKind returns the kind of a scalar type keyword such as "int32".
func ScalarKind(name string) (Kind, bool) {
	k, ok := scalarKinds[name]
	return k, ok
}

// IsScalar reports whether k is a scalar type (not message, group, enum or
// unknown).
func (k Kind) IsScalar() bool {
	return k != KindUnknown && k != KindGroup && k != KindMessage && k != KindEnum && int(k) < len(kindNames)
}

// IsPackable reports whether repeated fields of kind k may use the packed
// encoding: numeric scalars, bool and enums.
func (k Kind) IsPackable() bool {
	return k == KindEnum || k.IsScalar() && k != KindString && k != KindBytes
}

// Cardinality is a field's label after syntax and features are applied.
type Cardinality uint8

// Cardinalities.
const (
	Optional Cardinality = 1 // singular
	Required Cardinality = 2
	Repeated Cardinality = 3
)

func (c Cardinality) String() string {
	switch c {
	case Optional:
		return "optional"
	case Required:
		return "required"
	case Repeated:
		return "repeated"
	}
	return "Cardinality(" + strconv.Itoa(int(c)) + ")"
}
