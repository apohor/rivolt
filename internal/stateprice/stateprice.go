// Package stateprice is a dated, cited table of US average residential electricity prices by state. The guided setup offers a state's
// average as a one-tap default for the home rate (charging.home_price_source = state_average); the user can always type their own.
//
// The table is the U.S. Energy Information Administration's Electric Power Monthly, Table 5.6.B (average price of electricity to ultimate
// customers by end-use sector, by state), residential column, year to date. It is refreshed by a pull request: copy the new residential
// column, bump Period, Released and Checked, and the tests check the shape. Nothing is fetched at run time.
package stateprice

import "strings"

// Source describes where the table comes from and how fresh it is.
type Source struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Period   string `json:"period"`
	Released string `json:"released"` // the date EIA published the table
	Checked  string `json:"checked"`  // the date the numbers were copied here
}

// Table is the source of the numbers below.
var Table = Source{
	Name:     "U.S. EIA, Electric Power Monthly, Table 5.6.B: average price to ultimate customers, residential, year to date",
	URL:      "https://www.eia.gov/electricity/monthly/epm_table_grapher.php?t=epmt_5_6_b",
	Period:   "January to August 2026",
	Released: "2026-10-23",
	Checked:  "2026-11-13",
}

type entry struct {
	name  string
	cents float64 // residential average, cents per kWh
}

var byCode = map[string]entry{
	"AK": {"Alaska", 27.32},
	"AL": {"Alabama", 16.51},
	"AR": {"Arkansas", 13.74},
	"AZ": {"Arizona", 15.36},
	"CA": {"California", 32.58},
	"CO": {"Colorado", 16.80},
	"CT": {"Connecticut", 27.47},
	"DC": {"District of Columbia", 24.86},
	"DE": {"Delaware", 17.98},
	"FL": {"Florida", 15.32},
	"GA": {"Georgia", 15.48},
	"HI": {"Hawaii", 46.41},
	"IA": {"Iowa", 14.55},
	"ID": {"Idaho", 13.02},
	"IL": {"Illinois", 19.23},
	"IN": {"Indiana", 17.04},
	"KS": {"Kansas", 15.26},
	"KY": {"Kentucky", 14.20},
	"LA": {"Louisiana", 13.27},
	"MA": {"Massachusetts", 30.19},
	"MD": {"Maryland", 21.31},
	"ME": {"Maine", 30.23},
	"MI": {"Michigan", 21.73},
	"MN": {"Minnesota", 16.44},
	"MO": {"Missouri", 14.28},
	"MS": {"Mississippi", 15.06},
	"MT": {"Montana", 14.13},
	"NC": {"North Carolina", 14.94},
	"ND": {"North Dakota", 12.46},
	"NE": {"Nebraska", 12.92},
	"NH": {"New Hampshire", 26.91},
	"NJ": {"New Jersey", 24.09},
	"NM": {"New Mexico", 15.34},
	"NV": {"Nevada", 13.36},
	"NY": {"New York", 29.26},
	"OH": {"Ohio", 18.81},
	"OK": {"Oklahoma", 13.81},
	"OR": {"Oregon", 15.46},
	"PA": {"Pennsylvania", 21.13},
	"RI": {"Rhode Island", 29.05},
	"SC": {"South Carolina", 15.90},
	"SD": {"South Dakota", 14.58},
	"TN": {"Tennessee", 13.80},
	"TX": {"Texas", 16.03},
	"UT": {"Utah", 13.19},
	"VA": {"Virginia", 16.95},
	"VT": {"Vermont", 23.93},
	"WA": {"Washington", 14.42},
	"WI": {"Wisconsin", 19.05},
	"WV": {"West Virginia", 15.58},
	"WY": {"Wyoming", 14.01},
}

// Price is a state's average residential price in US dollars per kWh.
type Price struct {
	State       string  `json:"state"` // two-letter code
	Name        string  `json:"name"`
	PricePerKWh float64 `json:"price_per_kwh"`
	Currency    string  `json:"currency"`
	Source      Source  `json:"source"`
}

var byName = func() map[string]string {
	m := make(map[string]string, len(byCode))
	for code, e := range byCode {
		m[strings.ToLower(e.name)] = code
	}
	return m
}()

// Lookup finds a state by its two-letter code or its full name, any case. It reports false for anything else (other countries, territories,
// a blank). The price is rounded to the tenth of a cent, as published.
func Lookup(state string) (Price, bool) {
	k := strings.TrimSpace(state)
	code := strings.ToUpper(k)
	e, ok := byCode[code]
	if !ok {
		if code, ok = byName[strings.ToLower(k)]; !ok {
			return Price{}, false
		}
		e = byCode[code]
	}
	return Price{State: code, Name: e.name, PricePerKWh: e.cents / 100, Currency: "USD", Source: Table}, true
}

// Len is the number of states in the table (50 states and DC).
func Len() int { return len(byCode) }
