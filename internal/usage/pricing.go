// Package usage keeps persistent token totals, reads the subscription's rate
// limit headers, and estimates how much of the subscription routing saved.
package usage

import (
	"sort"
	"strings"
)

// Price is USD per million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

func price(in, out, cacheRead float64) Price {
	return Price{Input: in, Output: out, CacheRead: cacheRead, CacheWrite: in * 1.25}
}

// Anthropic API list prices, used only as weights: subscription usage is
// assumed to scale with what the same tokens would cost on the API.
// Matched by prefix, longest first, so dated ids work too.
var prices = map[string]Price{
	"claude-fable-5-1":  price(10, 50, 0.25),
	"claude-fable-5":    price(10, 50, 1.00),
	"claude-mythos-5":   price(10, 50, 0.25),
	"claude-opus-5-5":   price(4, 20, 0.20),
	"claude-opus-5":     price(5, 25, 0.50),
	"claude-opus-4-8":   price(5, 25, 0.50),
	"claude-opus-4-7":   price(5, 25, 0.50),
	"claude-opus-4-6":   price(5, 25, 0.50),
	"claude-opus-4-5":   price(5, 25, 0.50),
	"claude-opus-4":     price(15, 75, 1.50),
	"claude-3-opus":     price(15, 75, 1.50),
	"claude-sonnet-5-5": price(2, 10, 0.20),
	"claude-sonnet-5":   price(2, 10, 0.20),
	"claude-sonnet-4":   price(3, 15, 0.30),
	"claude-3-7-sonnet": price(3, 15, 0.30),
	"claude-3-5-sonnet": price(3, 15, 0.30),
	"claude-haiku-5-5":  price(0.10, 0.50, 0.01),
	"claude-haiku-4-5":  price(1, 5, 0.10),
	"claude-3-5-haiku":  price(0.80, 4, 0.08),
	"claude-3-haiku":    price(0.25, 1.25, 0.03),
}

// fallbackPrice is used for unknown models (e.g. "claude-test" or a model
// newer than this table).
var fallbackPrice = prices["claude-sonnet-5-5"]

var priceKeys = func() []string {
	keys := make([]string, 0, len(prices))
	for k := range prices {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}()

// PriceFor returns the API price for a Claude model id and whether it was known.
func PriceFor(model string) (Price, bool) {
	m := strings.ToLower(model)
	for _, k := range priceKeys {
		if strings.HasPrefix(m, k) {
			return prices[k], true
		}
	}
	return fallbackPrice, false
}

// Tokens is a token count split by kind. Input excludes cache reads/writes.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// Total is all tokens.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// AllInput is every input token, cached or not.
func (t Tokens) AllInput() int64 { return t.Input + t.CacheRead + t.CacheWrite }

// Cost is the API price of these tokens in USD.
func (t Tokens) Cost(p Price) float64 {
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.CacheRead + float64(t.CacheWrite)*p.CacheWrite) / 1e6
}

// Plan is a Claude subscription plan.
type Plan struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	PriceUSD float64 `json:"price_usd"` // per month
	Multiple float64 `json:"multiple"`  // usage relative to Pro
}

// Plans lists the known plans (prices as of late 2026).
var Plans = []Plan{
	{ID: "pro", Name: "Pro", PriceUSD: 20, Multiple: 1},
	{ID: "max5", Name: "Max 5x", PriceUSD: 100, Multiple: 5},
	{ID: "max20", Name: "Max 20x", PriceUSD: 200, Multiple: 20},
	{ID: "custom", Name: "Other", PriceUSD: 0, Multiple: 0},
}

// PlanByID finds a plan; customPrice overrides the price of "custom".
func PlanByID(id string, customPrice float64) (Plan, bool) {
	for _, p := range Plans {
		if p.ID == id {
			if p.ID == "custom" {
				p.PriceUSD = customPrice
			}
			return p, true
		}
	}
	return Plan{}, false
}
