package graph

// The narrowings this correlator implements. Each is a switch: the
// planner passes those in force at the run's level, and with one off the
// binding falls back to the wide rule.
const (
	// NarrowMethodSets binds a type through its methods' signatures, which
	// go-sbom folds into the type's digest, and binds only the methods
	// without statements, instead of every method of every type a slice
	// uses.
	NarrowMethodSets = "method-sets"
	// NarrowInterfaceMethods binds a type outside the tests through its
	// declaration and the signatures of the methods some interface of the
	// program or some lookup by name can reach, instead of through its
	// whole method set.
	NarrowInterfaceMethods = "interface-methods"
	// NarrowTestMethodSets binds a test type that never reaches an
	// interface through its own declaration and the methods its tests
	// call, instead of through its whole method set and every method.
	NarrowTestMethodSets = "test-method-sets"
	// NarrowPureInitialisers binds a named variable through the code that
	// uses it unless its initialiser calls outside the pure calls, instead
	// of binding every variable whose initialiser calls anything to every
	// slice linking its package.
	NarrowPureInitialisers = "pure-initialisers"
	// NarrowPureFunctions counts a call of a function of the tree whose
	// body only builds its result as a pure call, so a variable built by
	// the repository's own constructor binds through the code that uses
	// it, instead of counting only the configured pure calls.
	NarrowPureFunctions = "pure-functions"
	// NarrowPureTestVariables holds a test file's variables to the same
	// rule: bound through the tests that use them unless the initialiser
	// may act, instead of binding every slice of the package because each
	// is set up for every test.
	NarrowPureTestVariables = "pure-test-variables"
	// NarrowReferencedNew counts a declaration that a bound one references
	// as reached, so a new helper called by a changed function selects
	// through that function, instead of through its package's unbound
	// names.
	NarrowReferencedNew = "referenced-new"
	// NarrowUnreachedNew lets a new declaration nothing reaches select
	// nothing, instead of every slice linking its package.
	NarrowUnreachedNew = "unreached-new"
	// NarrowModuleBumps binds a slice to the modules of the packages its
	// binaries link, transitively, instead of to every module its main
	// modules require.
	NarrowModuleBumps = "module-bumps"
	// NarrowSetupSplit keeps what a batch's setup ran out of its slices'
	// bindings and binds it to a target of its own per suite, which the
	// planner samples, instead of to every slice.
	NarrowSetupSplit = "setup-split"
)

type Narrowing struct {
	Name string
	// OffAt is the lowest narrowing level that switches it off.
	OffAt int
	// Note says what it can miss and why that is acceptable.
	Note string
}

// Narrowings are the narrowings the correlator declares, riskiest first,
// at the levels of pipelinegen's ladder.
var Narrowings = []Narrowing{
	{
		Name: NarrowSetupSplit, OffAt: 3,
		Note: "code that starts lazily (a once, a warmed cache) shows in whichever slice ran first, and a change to " +
			"setup-only code can break any slice; the sample and the nightly run catch it",
	},
	{
		Name: NarrowUnreachedNew, OffAt: 3,
		Note: "a new declaration runs only through a changed caller, an init or an initialiser with effects; the " +
			"last two bind every slice linking the package anyway, but reflection or a registry filled elsewhere can " +
			"reach it unseen until something that selects changes",
	},
	{
		Name: NarrowInterfaceMethods, OffAt: 4,
		Note: "as sound as the set of names is complete: a method a type gains, loses or changes is seen by code " +
			"that never names it only through an interface the program declares with a method of that name, a " +
			"lookup by name or a template, or a selection that now resolves elsewhere through embedding, and each " +
			"binds it; reflection or template text the analysis can't read binds the whole method set of the types " +
			"that can reach it, or of every type when that can't be told",
	},
	{
		Name: NarrowMethodSets, OffAt: 5,
		Note: "sound: dynamic dispatch depends on a type's method set, which its digest holds; a method's body " +
			"matters only where it ran, and then the slice's list holds it",
	},
	{
		Name: NarrowTestMethodSets, OffAt: 5,
		Note: "sound by the method-sets argument for a test type no value of which reaches an interface: a method " +
			"runs only where code calls it, and that code binds it. Any interface-typed slot counts as reaching one " +
			"(a parameter, a field, an element, a return, a type parameter). A test type embedded in a named test type " +
			"escapes when that type does, since its methods are promoted; embedded anywhere else it escapes. A miss " +
			"in that check is the risk",
	},
	{
		Name: NarrowPureInitialisers, OffAt: 5,
		Note: "as sound as the list of pure calls: each must only build a value and act on nothing else; the " +
			"default's regexp.MustCompile and time.Date can panic on their arguments, and fmt's calls run the Error, " +
			"String and Format methods of theirs. An initialiser that can panic or block without a call binds wide",
	},
	{
		Name: NarrowPureFunctions, OffAt: 5,
		Note: "as sound as the proof, which reads only syntax: a plain function whose body is one return of " +
			"literals, names, composite literals with field names, constant indexes or literal map keys, " +
			"arithmetic that can't panic and calls of pure functions with pure arguments; anything else, a method, " +
			"a conversion, a builtin, a generic function, a cycle, a file with cgo or a go:linkname directive, acts",
	},
	{
		Name: NarrowPureTestVariables, OffAt: 5,
		Note: "as sound as pure-initialisers: a test variable whose initialiser only builds a value changes a test " +
			"only through the code that uses it, which binds it by reference",
	},
	{
		Name: NarrowReferencedNew, OffAt: 5,
		Note: "sound: a new declaration that a bound one references runs only through that referrer, whose change " +
			"selects the slice already",
	},
	{
		Name: NarrowModuleBumps, OffAt: 5,
		Note: "sound: a binary can run a module's code only by linking one of its packages, and go list names " +
			"every package each binary links, through other modules too",
	},
}
