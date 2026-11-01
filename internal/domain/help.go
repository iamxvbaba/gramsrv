package domain

// AppConfig is the client application configuration.
type AppConfig struct {
	Client string
	Hash   int
	JSON   []byte
}

// CountryCode is the phone dialing-code rule for a country.
type CountryCode struct {
	CountryCode string
	Prefixes    []string
	Patterns    []string
}

// Country is a country/dialing-code option on the login page.
type Country struct {
	ISO2         string
	DefaultName  string
	Name         string
	Hidden       bool
	CountryCodes []CountryCode
}

// CountriesList is the result of the help.getCountriesList query.
type CountriesList struct {
	Hash      int
	Countries []Country
}
