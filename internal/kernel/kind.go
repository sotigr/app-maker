package kernel

import "fmt"

// Kind is the kind of a field value (spec 3.2).
type Kind uint8

// Value kinds. KindNull is the kind of the null value only; no field has it.
const (
	KindInvalid Kind = iota
	KindString
	KindInt64
	KindFloat
	KindDecimal
	KindBool
	KindTime
	KindRef
	KindBlob
	KindObject
	KindList
	KindEnum
	KindJSON
	KindNull
)

var kindNames = [...]string{
	KindInvalid: "invalid",
	KindString:  "string",
	KindInt64:   "int64",
	KindFloat:   "float",
	KindDecimal: "decimal",
	KindBool:    "bool",
	KindTime:    "time",
	KindRef:     "ref",
	KindBlob:    "blob",
	KindObject:  "object",
	KindList:    "list",
	KindEnum:    "enum",
	KindJSON:    "json",
	KindNull:    "null",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// ParseKind parses a kind name as written in x-kind.
func ParseKind(s string) (Kind, bool) {
	for k, n := range kindNames {
		if n == s && Kind(k) != KindInvalid && Kind(k) != KindNull {
			return Kind(k), true
		}
	}
	return KindInvalid, false
}

// IsScalar reports whether values of k are scalars (not Object, List, JSON).
func (k Kind) IsScalar() bool {
	switch k {
	case KindString, KindInt64, KindFloat, KindDecimal, KindBool, KindTime, KindRef, KindEnum:
		return true
	}
	return false
}

// IsOrdered reports whether k supports range comparisons (spec 7.2).
func (k Kind) IsOrdered() bool {
	switch k {
	case KindString, KindInt64, KindFloat, KindDecimal, KindTime:
		return true
	}
	return false
}
