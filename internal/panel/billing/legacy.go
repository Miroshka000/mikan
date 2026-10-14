package billing

// YooKassa and CryptoBot were built into the panel until 0.4.3; since 0.4.4 they are the
// marketplace's adapters of the same names. The webhook URLs set in their dashboards and
// the bot's old pay buttons still lead to the adapters.

// The built-in providers' names, as payments, webhook URLs and settings keep them.
const (
	legacyYooKassa  = "yookassa"
	legacyCryptoBot = "cryptobot"
)

// AdapterOf is the adapter that took over a built-in provider's name, the name itself
// otherwise.
func AdapterOf(provider string) string {
	if provider == legacyYooKassa || provider == legacyCryptoBot {
		return AddonPrefix + provider
	}
	return provider
}
