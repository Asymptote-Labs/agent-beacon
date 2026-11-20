package threatrules

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Rules read the event through cel-go's native-types extension, which finds a field by comparing
// the CEL name of every field of the struct, parsing struct tags, on every read, and rebuilds a
// reflected type description each time a struct value crosses into CEL. That was most of the cost
// of evaluating a rule against a small event. The types here do the same with field indexes
// worked out once per type, and are what Env installs:
//
//   - fastProvider is the extension's type provider, except that a type-checked field selection,
//     which is how rules read the event, reads the field by index.
//   - fastObj is the extension's object value. Field selection and presence take the indexed path
//     with the extension's exact semantics (getFieldValue below); anything else, from an undefined
//     field to equality and conversion, is handed to the extension's own value, built on demand,
//     so results and error messages are its. It keeps the value of each field it has read, so
//     the rules of a scan, which all read the same event, turn it into CEL values once.
//
// newFastAdapter checks every struct type against the extension's provider and fails if a field
// name disagrees; Env then keeps the extension's own adapter and provider.
type fastObj struct {
	t   *fastType
	val any           // the struct or a pointer to it, as given
	rv  reflect.Value // the struct, pointers followed
	// read holds the value of each field that has been read, by the field's slot. An object is
	// read by one goroutine at a time, as the activation it belongs to is.
	read []ref.Val
}

// fastType is what fastObj needs about one struct type.
type fastType struct {
	a   *fastAdapter
	cel ref.Type // the extension's type for the struct
	// fields are the fields by CEL name; nil for a name the extension leaves undefined.
	fields map[string]*fastField
	slots  int // how many fields have a slot in fastObj.read
	// sized are the indexes of the fields a rule can address, the ones whose sizes bound what it
	// reads (matcher.go).
	sized []int
}

// fastField is one field of a struct type: where it is in the struct, and in fastObj.read.
type fastField struct {
	index []int
	slot  int
}

// fastAdapter adapts values for rules: structs of the event's types become fastObjs, lists and maps
// adapt their elements with it, and everything else goes to the extension's adapter.
type fastAdapter struct {
	base     types.Adapter
	types    map[reflect.Type]*fastType
	provider *fastProvider
}

// fastProvider is the extension's type provider with the fields of the event's types read by
// index. Types and field types are the extension's.
type fastProvider struct {
	types.Provider
	a      *fastAdapter
	fields map[[2]string]*types.FieldType // (CEL type name, field name)
}

func (p *fastProvider) FindStructFieldType(structType, fieldName string) (*types.FieldType, bool) {
	if ft, ok := p.fields[[2]string{structType, fieldName}]; ok {
		return ft, true
	}
	return p.Provider.FindStructFieldType(structType, fieldName)
}

// NewValue builds a struct literal as the extension does, as a fastObj, so that it compares equal
// to the event's values exactly when the extension's would.
func (p *fastProvider) NewValue(typeName string, fields map[string]ref.Val) ref.Val {
	v := p.Provider.NewValue(typeName, fields)
	if types.IsError(v) {
		return v
	}
	return p.a.NativeToValue(v.Value())
}

// indexedField is the extension's field type for one field of ft. Of a fastObj, which is what a
// rule selects from, it gives what the object holds for the field (fastObj.get); of the struct
// itself it reads the field by index with the extension's getFieldValue.
func indexedField(base *types.FieldType, ft *fastType, f *fastField) *types.FieldType {
	native := func(obj any) reflect.Value { return reflect.Indirect(reflect.ValueOf(obj)).FieldByIndex(f.index) }
	return &types.FieldType{
		Type:        base.Type,
		IsJSONField: base.IsJSONField,
		IsSet: func(obj any) bool {
			if o, ok := obj.(*fastObj); ok {
				if o.t == ft {
					return !o.rv.FieldByIndex(f.index).IsZero()
				}
				obj = o.val
			}
			return !native(obj).IsZero()
		},
		GetFrom: func(obj any) (any, error) {
			if o, ok := obj.(*fastObj); ok {
				if o.t == ft {
					return o.get(f), nil
				}
				obj = o.val
			}
			return getFieldValue(native(obj)), nil
		},
	}
}

// newFastAdapter describes every struct type reachable from the event, checking each against the
// environment's provider, and fails when a field name disagrees.
func newFastAdapter(env *cel.Env) (*fastAdapter, error) {
	provider := env.CELTypeProvider()
	a := &fastAdapter{base: env.CELTypeAdapter(), types: map[reflect.Type]*fastType{}}
	a.provider = &fastProvider{Provider: provider, a: a, fields: map[[2]string]*types.FieldType{}}
	var walk func(t reflect.Type) error
	walk = func(t reflect.Type) error {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			return walk(t.Elem())
		case reflect.Struct:
		default:
			return nil
		}
		if _, done := a.types[t]; done || t == reflect.TypeFor[time.Time]() {
			return nil
		}
		ft := &fastType{a: a, cel: a.base.NativeToValue(reflect.New(t).Interface()).Type(), fields: map[string]*fastField{}}
		a.types[t] = ft
		var names []string
		for i := range t.NumField() {
			f := t.Field(i)
			name := celFieldName(f)
			if name == "" || name == "-" {
				continue
			}
			if _, dup := ft.fields[name]; dup {
				continue // the extension takes the first field with a name
			}
			names = append(names, name)
			if !f.IsExported() || !supportedType(f.Type) {
				ft.fields[name] = nil
				continue
			}
			field := &fastField{index: f.Index, slot: ft.slots}
			ft.slots++
			ft.fields[name] = field
			if base, ok := provider.FindStructFieldType(ft.cel.TypeName(), name); ok {
				a.provider.fields[[2]string{ft.cel.TypeName(), name}] = indexedField(base, ft, field)
				ft.sized = append(ft.sized, i)
			}
			if err := walk(f.Type); err != nil {
				return err
			}
		}
		want, ok := provider.FindStructFieldNames(ft.cel.TypeName())
		if !ok || !sameSet(names, want) {
			return fmt.Errorf("fields of %s disagree with the CEL environment: %v vs %v", ft.cel.TypeName(), names, want)
		}
		return nil
	}
	if err := walk(reflect.TypeFor[asymptoteobserve.Event]()); err != nil {
		return nil, err
	}
	return a, nil
}

// NativeToValue mirrors the extension's, with fastObj for the event's struct types.
func (a *fastAdapter) NativeToValue(val any) ref.Val {
	if val == nil {
		return types.NullValue
	}
	if v, ok := val.(ref.Val); ok {
		return v
	}
	sv := reflect.Indirect(reflect.ValueOf(val))
	switch sv.Kind() {
	case reflect.Struct:
		if ft, ok := a.types[sv.Type()]; ok {
			return &fastObj{t: ft, val: val, rv: sv}
		}
	case reflect.Slice, reflect.Array:
		if sv.Type().Elem().Kind() != reflect.Uint8 {
			return types.NewDynamicList(a, val)
		}
	case reflect.Map:
		return types.NewDynamicMap(a, val)
	}
	return a.base.NativeToValue(val)
}

// adapt is the CEL value of a field of an object (field set), or of an element of a list. It is
// NativeToValue's, of what the extension reads the field as (getFieldValue), except that a list
// is adapted whole, here, into a list of CEL values: the one NativeToValue gives adapts an
// element each time it is reached, which for a struct copies it. Whatever is adapted here is
// kept by the object it was read from, so that happens once.
func (a *fastAdapter) adapt(v reflect.Value, field bool) ref.Val {
	switch v.Kind() {
	case reflect.String:
		if v.Type() == reflect.TypeFor[string]() {
			return types.String(v.String())
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			elems := make([]ref.Val, v.Len())
			for i := range elems {
				elems[i] = a.adapt(v.Index(i), false)
			}
			return types.NewRefValList(a, elems)
		}
	}
	if field {
		return a.NativeToValue(getFieldValue(v))
	}
	return a.NativeToValue(v.Interface())
}

// slow is the extension's own value for o.
func (o *fastObj) slow() ref.Val { return o.t.a.base.NativeToValue(o.val) }

func (o *fastObj) field(name ref.Val) *fastField {
	s, ok := name.(types.String)
	if !ok {
		return nil
	}
	return o.t.fields[string(s)]
}

// get is the field's value as rules see it. It is worked out once: an object stands for one
// event of one scan, every rule of the scan reads the same few fields of it, and what took the
// time was turning each into a CEL value again for each rule, a list of commands and each of
// their words included.
func (o *fastObj) get(f *fastField) ref.Val {
	if o.read == nil {
		o.read = make([]ref.Val, o.t.slots)
	}
	v := o.read[f.slot]
	if v == nil {
		v = o.t.a.adapt(o.rv.FieldByIndex(f.index), true)
		o.read[f.slot] = v
	}
	return v
}

// Get implements traits.Indexer as the extension's object does.
func (o *fastObj) Get(name ref.Val) ref.Val {
	f := o.field(name)
	if f == nil {
		return o.slow().(interface{ Get(ref.Val) ref.Val }).Get(name)
	}
	return o.get(f)
}

// IsSet implements traits.FieldTester as the extension's object does.
func (o *fastObj) IsSet(name ref.Val) ref.Val {
	f := o.field(name)
	if f == nil {
		return o.slow().(interface{ IsSet(ref.Val) ref.Val }).IsSet(name)
	}
	return types.Bool(!o.rv.FieldByIndex(f.index).IsZero())
}

func (o *fastObj) IsZeroValue() bool { return o.rv.IsZero() }
func (o *fastObj) Type() ref.Type    { return o.t.cel }

// Value is the object itself, where the extension's is the struct. cel-go selects a field by
// handing a field type the Value of what it is selected from, and a field type of these
// (indexedField) can then give what the object has already read. A rule cannot see the
// difference: the struct is reached only by converting the object, which goes to the extension.
func (o *fastObj) Value() any { return o }

func (o *fastObj) ConvertToNative(t reflect.Type) (any, error) { return o.slow().ConvertToNative(t) }

// ConvertToType is the extension's, which returns the object itself for its own type.
func (o *fastObj) ConvertToType(t ref.Type) ref.Val {
	slow := o.slow()
	v := slow.ConvertToType(t)
	if v == slow {
		return o
	}
	return v
}

func (o *fastObj) Equal(other ref.Val) ref.Val {
	if f, ok := other.(*fastObj); ok {
		other = f.slow()
	}
	return o.slow().Equal(other)
}

// getFieldValue is the extension's: a zero time is the Unix epoch, and a nil pointer is a pointer
// to a zero struct, which is what makes `e.command.command` "" rather than an error on an event
// without a command.
func getFieldValue(f reflect.Value) any {
	if f.IsZero() {
		switch f.Kind() {
		case reflect.Struct:
			if f.Type() == reflect.TypeFor[time.Time]() {
				return time.Unix(0, 0)
			}
		case reflect.Pointer:
			return reflect.New(f.Type().Elem()).Interface()
		}
	}
	return f.Interface()
}

// supportedType is the extension's test for a field it can expose.
func supportedType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Chan, reflect.Complex64, reflect.Complex128, reflect.Func, reflect.UnsafePointer, reflect.Uintptr:
		return false
	case reflect.Array, reflect.Slice:
		return supportedType(t.Elem())
	case reflect.Map:
		return supportedType(t.Key()) && supportedType(t.Elem())
	}
	return true
}

func sameSet(a, b []string) bool {
	return len(a) == len(b) && !slices.ContainsFunc(b, func(s string) bool { return !slices.Contains(a, s) })
}
