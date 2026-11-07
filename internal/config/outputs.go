package config

import "strings"

// The outputs a deploy adds to the configuration's own, from what it deploys.
const (
	// OutputHTTPAPIURL is the url of the http api, or https://<domain> with a domain.
	OutputHTTPAPIURL = "httpApiUrl"
	// OutputHTTPDomainTarget is where the dns record of the http api's domain points.
	OutputHTTPDomainTarget = "httpDomainTarget"
	// OutputRESTAPIURL is the url of the rest api's stage.
	OutputRESTAPIURL = "restApiUrl"
	// OutputRESTAPIKeyID is the id of the api key of the rest api's private routes.
	OutputRESTAPIKeyID = "restApiKeyId"
	// OutputWebSocketURL is the url of the websocket api's stage.
	OutputWebSocketURL = "webSocketUrl"
)

// URLOutput is the name of the output of a function's url: the function's name of dashes in
// camel case, then Url, stripeWebhookUrl for stripe-webhook.
func URLOutput(function string) string {
	words := strings.Split(function, "-")
	for i, w := range words[1:] {
		if w != "" {
			words[i+1] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, "") + "Url"
}

// GeneratedOutputs are the outputs a deploy of the project adds, by name, each with what it is:
// a <function>Url for each function url, httpApiUrl once a function has an http route, with
// httpDomainTarget when the http api has a domain, restApiUrl once one has a rest route, with
// restApiKeyId when one is private, and webSocketUrl once one has a websocket route.
func (p *Project) GeneratedOutputs() map[string]string {
	out := map[string]string{}
	routes, rest, private, websocket := false, false, false, false
	for name, fn := range p.Functions {
		if fn.URL != nil {
			out[URLOutput(name)] = "the url of function " + name
		}
		for _, e := range fn.Events {
			routes = routes || e.Type == EventHTTPRoute
			rest = rest || e.Type == EventRESTRoute
			private = private || (e.Type == EventRESTRoute && e.Private)
			websocket = websocket || e.Type == EventWebSocket
		}
	}
	if routes {
		out[OutputHTTPAPIURL] = "the url of the http api"
		if p.HTTP != nil && p.HTTP.Domain != "" {
			out[OutputHTTPDomainTarget] = "where the dns record of the http api's domain points"
		}
	}
	if rest {
		out[OutputRESTAPIURL] = "the url of the rest api"
	}
	if private {
		out[OutputRESTAPIKeyID] = "the id of the api key of the rest api's private routes"
	}
	if websocket {
		out[OutputWebSocketURL] = "the url of the websocket api"
	}
	return out
}

// notOutput names what in an output a deploy cannot resolve from the state, a secret or
// kdst.stageOutput(), in it or in a join of it, or is empty when there is none.
func notOutput(v Value) string {
	switch {
	case v.Secret != nil:
		return "a secret"
	case v.Output != nil:
		return "kdst.stageOutput()"
	}
	for _, part := range v.Join {
		if what := notOutput(part); what != "" {
			return what
		}
	}
	return ""
}
