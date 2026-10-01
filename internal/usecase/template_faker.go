package usecase

import (
	"math/rand"
	"strconv"
	"strings"
)

const (
	defaultFakerIntMax = 100
	fakerFloatDecimals = 2
	defaultSentenceLen = 6
	fakerPhoneDigits   = 7
	fakerPhoneMax      = 10
	fakerSuffixMax     = 1000
	fakerEmailSuffix   = 100
	boolChoices        = 2
	rangeArgs          = 2
)

var (
	fakerFirstNames = []string{
		"Ann", "Bob", "Carla", "David", "Elena", "Frank", "Grace", "Hugo", "Irene", "Jack",
		"Kira", "Liam", "Maya", "Noah", "Olga", "Paul", "Rita", "Sam", "Tina", "Victor",
	}
	fakerLastNames = []string{
		"Smith", "Johnson", "Brown", "Taylor", "Miller", "Davis", "Wilson", "Moore", "Clark", "Lewis",
		"Walker", "Hall", "Young", "King", "Wright", "Scott", "Green", "Baker", "Adams", "Nelson",
	}
	fakerCities = []string{
		"Berlin", "Paris", "Madrid", "Rome", "Vienna", "Oslo", "Lisbon", "Prague", "Warsaw", "Dublin",
	}
	fakerCountries = []string{
		"Germany", "France", "Spain", "Italy", "Austria", "Norway", "Portugal", "Poland", "Ireland", "Sweden",
	}
	fakerCompanies = []string{
		"Acme", "Globex", "Initech", "Umbrella", "Hooli", "Stark", "Wayne", "Wonka", "Soylent", "Cyberdyne",
	}
	fakerWords = []string{
		"alpha", "river", "lantern", "orbit", "maple", "quartz", "harbor", "ember", "pixel", "meadow",
		"signal", "copper", "willow", "falcon", "garden", "thunder", "velvet", "summit", "breeze", "anchor",
	}
)

// fakerFuncs maps {{faker.<fn>}} to its generator; a generator reports false
// for bad arguments, so the placeholder stays as written.
var fakerFuncs = map[string]func(args []string) (any, bool){
	"name":      func([]string) (any, bool) { return pick(fakerFirstNames) + " " + pick(fakerLastNames), true },
	"firstName": func([]string) (any, bool) { return pick(fakerFirstNames), true },
	"lastName":  func([]string) (any, bool) { return pick(fakerLastNames), true },
	"username": func([]string) (any, bool) {
		return strings.ToLower(pick(fakerFirstNames)) + strconv.Itoa(randInt(fakerSuffixMax)), true
	},
	"email":    func([]string) (any, bool) { return fakerEmail(), true },
	"phone":    func([]string) (any, bool) { return fakerPhone(), true },
	"city":     func([]string) (any, bool) { return pick(fakerCities), true },
	"country":  func([]string) (any, bool) { return pick(fakerCountries), true },
	"company":  func([]string) (any, bool) { return pick(fakerCompanies), true },
	"word":     func([]string) (any, bool) { return pick(fakerWords), true },
	"sentence": fakerSentence,
	"bool":     func([]string) (any, bool) { return randInt(boolChoices) == 1, true },
	"pick": func(args []string) (any, bool) {
		if len(args) == 0 {
			return nil, false
		}

		return pick(args), true
	},
	"int":   func(args []string) (any, bool) { return fakerNumber("int", args) },
	"float": func(args []string) (any, bool) { return fakerNumber("float", args) },
}

// fakerValue evaluates {{faker.<fn> args...}}.
func fakerValue(fn string, args []string) (v any, ok bool) {
	gen, found := fakerFuncs[fn]
	if !found {
		return nil, false
	}

	return gen(args)
}

func pick(list []string) string {
	return list[randInt(len(list))]
}

func fakerEmail() string {
	return strings.ToLower(pick(fakerFirstNames)) + "." + strings.ToLower(pick(fakerLastNames)) +
		strconv.Itoa(randInt(fakerEmailSuffix)) + "@example.com"
}

func fakerPhone() string {
	var digits strings.Builder

	for range fakerPhoneDigits {
		digits.WriteString(strconv.Itoa(randInt(fakerPhoneMax)))
	}

	d := digits.String()

	return "+1-555-" + d[:3] + "-" + d[3:]
}

func fakerSentence(args []string) (any, bool) {
	n := defaultSentenceLen

	if len(args) > 0 {
		var err error

		n, err = strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return nil, false
		}
	}

	words := make([]string, n)
	for i := range words {
		words[i] = pick(fakerWords)
	}

	s := strings.Join(words, " ")

	return strings.ToUpper(s[:1]) + s[1:] + ".", true
}

// fakerNumber handles faker.int and faker.float with an optional min and max.
func fakerNumber(fn string, args []string) (any, bool) {
	lo, hi := 0.0, float64(defaultFakerIntMax)
	if fn == "float" {
		hi = 1
	}

	if len(args) == rangeArgs {
		var err1, err2 error

		lo, err1 = strconv.ParseFloat(args[0], 64)
		hi, err2 = strconv.ParseFloat(args[1], 64)

		if err1 != nil || err2 != nil || hi < lo {
			return nil, false
		}
	} else if len(args) != 0 {
		return nil, false
	}

	if fn == "int" {
		span := int64(hi) - int64(lo) + 1

		return int64(lo) + randInt64(span), true
	}

	f := lo + randFloat()*(hi-lo)
	p, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', fakerFloatDecimals, 64), 64)

	return p, true
}

// Mock data does not need a cryptographically secure source.

func randInt(n int) int { return rand.Intn(n) } //nolint:gosec // mock data.

func randInt64(n int64) int64 { return rand.Int63n(n) } //nolint:gosec // mock data.

func randFloat() float64 { return rand.Float64() } //nolint:gosec // mock data.
