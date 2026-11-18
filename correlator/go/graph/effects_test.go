package graph

import (
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// startFile holds one variable for each way an initialiser can panic or
// block without a call, and one for each way it can't.
const startFile = `package money

import "unsafe"

type Shape interface{ Area() int }

type Box struct{ Side int }

func (b Box) Area() int { return b.Side * b.Side }

type Pair struct{ K any }

var (
	nums   = []int{1, 2, 3}
	arr    = [3]int{1, 2, 3}
	ptr    *Box
	shape  Shape
	count  = 3
	shift  = 2
	ushift uint = 2
	key    any = 1
	ch     chan int
	text   = "abc"
	m      = map[string]int{"a": 1}
)

var (
	Index      = nums[1]
	Slice      = nums[1:]
	Deref      = *ptr
	Field      = ptr.Side
	Assert     = shape.(Box)
	Quo        = 10 / count
	Rem        = 10 % count
	Shl        = 1 << shift
	Compare    = shape == Shape(Box{})
	Receive    = <-ch
	ToArray    = [3]int(nums)
	MakeSized  = make([]int, count)
	MapKey     = map[any]int{key: 1}
	PairKey    = map[Pair]int{{K: key}: 1}
	MethodVal  = shape.Area
	UnsafeStr  = unsafe.String(unsafe.StringData(text), count)
)

var (
	ArrayConst   = arr[1]
	ArraySlice   = arr[:2]
	MapIndex     = m["a"]
	QuoConst     = count / 3
	FloatQuo     = 1.5 / float64(count)
	UShl         = 1 << ushift
	NilCompare   = shape == nil
	Literal      = Box{Side: count}
	Pointer      = &Box{Side: count}
	MakeConst    = make([]int, 3)
	StringIndex  = "abc"[1]
	StringConcat = text + "d"
	Lazy         = func() int { return *ptr }
	Got, OK      = shape.(Box)
	MapConstKey  = map[any]int{"a": 1}
	MethodExpr   = Box.Area
	BoxMethod    = Box{}.Area
)
`

func TestInitialisersThatCanPanicAct(t *testing.T) {
	root := fixture(t)
	write(t, root, "shop/money/start.go", startFile)
	tree := load(t, root)

	acting := []string{
		"Index", "Slice", "Deref", "Field", "Assert", "Quo", "Rem", "Shl", "Compare", "Receive", "ToArray",
		"MakeSized", "MapKey", "PairKey", "MethodVal", "UnsafeStr",
	}
	quiet := []string{
		"ArrayConst", "ArraySlice", "MapIndex", "QuoConst", "FloatQuo", "UShl", "NilCompare", "Literal", "Pointer",
		"MakeConst", "StringIndex", "StringConcat", "Lazy", "Got,OK", "MapConstKey", "MethodExpr", "BoxMethod",
	}

	for _, names := range [][]string{acting, quiet} {
		for _, n := range names {
			i, ok := tree.byID[goid.New("shop/money", goid.Var, n)]
			if !ok {
				t.Fatalf("no variable %s", n)
			}

			want := &names[0] == &acting[0]
			if d := tree.Decls[i]; d.Init != want || d.InitAny != want {
				t.Errorf("%s: Init %v, InitAny %v, want %v", n, d.Init, d.InitAny, want)
			}
		}
	}
}
