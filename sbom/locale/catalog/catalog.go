// Package catalog is shared by locale-sbom and locale-correlator, so they agree on every
// unit name and on which files are catalogs.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"slices"
	"strings"
)

const Namespace = "locale"

// Narrowing is the narrowing under which a slice that depends on a claimed
// catalog file depends on the keys of it the slice was seen to look up.
const Narrowing = "locale-keys"

// NoNS is the namespace of a catalog with one file per language.
const NoNS = "-"

const (
	KindCatalog   = "catalog"
	KindLanguage  = "language"
	KindNamespace = "namespace"
	KindKey       = "key"
	KindObject    = "object"
)

type File struct {
	Path    string
	Catalog string
	Lang    string
	NS      string
}

// ID is the catalog's own unit: its digest never changes, and a
// recording that holds it says a recorder ran for this catalog.
func ID(cat string) string { return Namespace + ":" + cat }

// LangID is the unit of a language being in the catalog.
func LangID(cat, lang string) string { return ID(cat) + "#" + lang }

// NSID is the unit of a namespace file being in the catalog.
func NSID(cat, lang, ns string) string { return LangID(cat, lang) + "/" + ns }

// KeyID is the unit of one key's meaning; the empty key is the whole
// namespace.
func KeyID(cat, lang, ns, key string) string { return NSID(cat, lang, ns) + ":" + key }

// Group is a key unit's group: the catalog, language, namespace and the
// key's first part, so a slice's many keys fall into few groups.
func Group(cat, lang, ns, key string) string {
	top, _, _ := strings.Cut(key, ".")

	return cat + "#" + lang + "/" + ns + ":" + top
}

var presentDigest = Digest([]byte("present 1"))

// PresentDigest is the digest of the catalog, language and namespace
// units.
func PresentDigest() string { return presentDigest }

// Digest is the first 128 bits of a SHA-256 in hex: plenty to tell a
// catalog's hundred thousand units apart.
func Digest(b []byte) string {
	h := sha256.Sum256(b)

	return hex.EncodeToString(h[:16])
}

var catalogDirs = []string{"locales", "locale", "i18n", "lang", "langs", "translations"}

// Locate names a path's catalog, language and namespace from its layout
// alone; content is Claimable's business.
func Locate(p string) (File, bool) {
	if Why(p) != "" || strings.ToLower(path.Ext(p)) != ".json" {
		return File{}, false
	}

	dir, name := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	stem := strings.TrimSuffix(name, path.Ext(name))

	// <catalog>/<lang>/<ns>.json
	if parent := path.Base(dir); dir != "" && IsLanguage(parent) && path.Dir(dir) != "." {
		cat := path.Dir(dir)
		if underCatalogDir(cat) && validNS(stem) {
			return File{Path: p, Catalog: cat, Lang: parent, NS: stem}, true
		}
	}

	// <catalog>/<lang>.json
	if dir != "" && IsLanguage(stem) && underCatalogDir(dir) {
		return File{Path: p, Catalog: dir, Lang: stem, NS: NoNS}, true
	}

	return File{}, false
}

// FilePath is where a catalog keeps a language's namespace, in the layout
// one of its files shows (nsLayout: one directory per language).
func FilePath(cat, lang, ns string, nsLayout bool) string {
	if nsLayout {
		return cat + "/" + lang + "/" + ns + ".json"
	}

	return cat + "/" + lang + ".json"
}

func validNS(s string) bool {
	return s != "" && s != NoNS && !strings.ContainsAny(s, "#:/")
}

func underCatalogDir(dir string) bool {
	return slices.ContainsFunc(strings.Split(dir, "/"), func(d string) bool {
		return slices.Contains(catalogDirs, strings.ToLower(d))
	})
}

// languageTag matches a BCP 47 tag's language, script and region (en, pt-BR,
// zh-Hans, zh_Hant_TW, es-419).
var languageTag = regexp.MustCompile(`^([a-zA-Z]{2,3})(?:[-_][a-zA-Z]{4})?(?:[-_](?:[a-zA-Z]{2}|[0-9]{3}))?$`)

// IsLanguage tells whether a name is a language tag with an ISO 639-1 code or
// a common three-letter one. The list keeps directory names like "ui" out.
func IsLanguage(s string) bool {
	m := languageTag.FindStringSubmatch(s)
	if m == nil {
		return false
	}

	lang := strings.ToLower(m[1])
	if len(lang) == 2 {
		return strings.Contains(iso6391, " "+lang+" ")
	}

	return slices.Contains(threeLetter, lang)
}

const iso6391 = " aa ab ae af ak am an ar as av ay az ba be bg bh bi bm bn bo br bs ca ce ch co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy ga gd gl gn gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu ja jv ka kg ki kj kk kl km kn ko kr ks ku kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl sm sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zh zu "

var threeLetter = []string{"ast", "bal", "ber", "ceb", "chr", "ckb", "dsb", "fil", "fur", "gsw", "haw", "hsb", "kab", "kok", "lij", "mai", "mni", "nap", "nds", "pcm", "sat", "scn", "sco", "szl", "tzm", "vec", "yue", "zgh"}
