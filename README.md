<div align="center">

<h1>CLAN</h1>

<p><strong>Cooperative LLM Access Network</strong></p>

<p><em>One account hits its limit — the next one picks up.</em></p>

</div>

---

CLAN is a self-hosted gateway that puts a single OpenAI-compatible endpoint in front of
every LLM account you own — CLI subscriptions and plain API keys alike.

Accounts join a shared pool, and each request leases one. When an account runs into a rate
limit, an expired token or an exhausted quota, the next account picks the request up. The
caller never sees any of it: one endpoint, one key, one model name.

> **Status:** early. The design is still being worked out and there is no usable build yet.
