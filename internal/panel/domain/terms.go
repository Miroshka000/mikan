package domain

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"mikan/internal/panel/store/db"
)

// What a term may cost. Traffic packages and the trial are sold on the same scale, so the
// API, the admin pages and these checks all mean the same numbers.
const (
	MinPriceStars = 1
	MaxPriceStars = 10_000
	MinPriceRub   = 100 // kopecks: a ruble
	MaxPriceRub   = 100_000_000
)

// PriceStarsOK, PriceRubOK: n is a price the panel sells at. No price at all (0: not sold
// in that currency) is not one, and whether it is allowed is the caller's.
func PriceStarsOK(n int64) bool { return n >= MinPriceStars && n <= MaxPriceStars }
func PriceRubOK(n int64) bool   { return n >= MinPriceRub && n <= MaxPriceRub }

// CheckPrices: a pair of prices as a tariff's; an unset price is not sold in that currency
// and passes. The error names the price that is out of range.
func CheckPrices(stars, rub sql.NullInt64) error {
	switch {
	case stars.Valid && !PriceStarsOK(stars.Int64):
		return fieldErr("price_stars", "bad_price")
	case rub.Valid && !PriceRubOK(rub.Int64):
		return fieldErr("price_rub", "bad_price")
	}
	return nil
}

// Term is one way a tariff is sold: for so many days (0: no end date; with a billing day,
// months of 30 days each) at these prices.
type Term struct {
	Days       int64
	PriceStars sql.NullInt64 // not for Stars when invalid
	PriceRub   sql.NullInt64 // kopecks; not for rubles when invalid
}

// TariffTerms are the terms tariff t is sold for, in order: its rows in tariff_terms, or,
// without any, the one term in the tariff itself. The first is the tariff's default term.
func TariffTerms(t db.Tariff, rows []db.TariffTerm) []Term {
	var out []Term
	for _, r := range rows {
		if r.TariffID == t.ID {
			out = append(out, Term{Days: r.Days, PriceStars: r.PriceStars, PriceRub: r.PriceRub})
		}
	}
	if len(out) == 0 {
		out = []Term{{Days: t.DurationDays, PriceStars: t.PriceStars, PriceRub: t.PriceRub}}
	}
	return out
}

// TermsOf reads the terms tariff t is sold for.
func TermsOf(ctx context.Context, q *db.Queries, t db.Tariff) ([]Term, error) {
	rows, err := q.ListTariffTerms(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return TariffTerms(t, rows), nil
}

// FindTerm is the term of so many days among terms.
func FindTerm(terms []Term, days int64) (Term, bool) {
	for _, t := range terms {
		if t.Days == days {
			return t, true
		}
	}
	return Term{}, false
}

// SetTariffTerms writes the terms of a tariff whose own columns already hold the first
// one: one term needs no rows, two or more are stored in order.
func SetTariffTerms(ctx context.Context, q *db.Queries, tariffID int64, terms []Term) error {
	if err := q.DeleteTariffTerms(ctx, tariffID); err != nil {
		return err
	}
	if len(terms) < 2 {
		return nil
	}
	for i, t := range terms {
		if err := q.AddTariffTerm(ctx, db.AddTariffTermParams{TariffID: tariffID, Days: t.Days, PriceStars: t.PriceStars, PriceRub: t.PriceRub, Sort: int64(i)}); err != nil {
			return err
		}
	}
	return nil
}

// termDays is the term bought on tariff t: the payment's, or the tariff's own.
func termDays(t db.Tariff, term sql.NullInt64) int64 {
	if term.Valid {
		return term.Int64
	}
	return t.DurationDays
}

// What a tariff's description may hold: it goes under the tariff's name in the bot and in
// the Mini App, so a screenful at most.
const (
	MaxDescriptionRunes = 300
	MaxDescriptionLines = 5
)

// CleanDescription is the admin's text for buyers as it is stored: line breaks as \n, the
// ends trimmed. A text over MaxDescriptionRunes characters or MaxDescriptionLines lines is
// refused, and the error names the field as the API does; empty is allowed (nothing to add).
func CleanDescription(s string) (string, error) {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s))
	if utf8.RuneCountInString(s) > MaxDescriptionRunes {
		return "", fieldErr("description", "description_too_long")
	}
	if s != "" && strings.Count(s, "\n")+1 > MaxDescriptionLines {
		return "", fieldErr("description", "description_too_many_lines")
	}
	return s, nil
}
