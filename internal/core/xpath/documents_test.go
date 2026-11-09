package xpath

import (
	"testing"
)

// The tests in this file run the supported features on realistic
// documents (testdata): each case lists the expected node paths or value.

func TestRSS(t *testing.T) {
	const ch = "/rss[1]/channel[1]"
	runCases(t, sample(t, "rss.xml"), Options{}, []queryCase{
		// Absolute and relative location paths, positions.
		{`/rss/channel/title`, ns(ch + "/title[1]")},
		{`/rss/channel/item/title`, ns(ch+"/item[1]/title[1]", ch+"/item[2]/title[1]", ch+"/item[3]/title[1]")},
		{`//item[2]/title`, ns(ch + "/item[2]/title[1]")},
		{`//item[last()]/title`, ns(ch + "/item[3]/title[1]")},
		{`//item[last() - 1]`, ns(ch + "/item[2]")},
		{`//item[position() < 3]/link`, ns(ch+"/item[1]/link[1]", ch+"/item[2]/link[1]")},
		{`//category[3]`, nil}, // [3] applies per parent
		{`(//category)[3]`, ns(ch + "/item[2]/category[1]")},
		{`/rss/channel/item[1]/category[2]`, ns(ch + "/item[1]/category[2]")},
		// Lenient names: "link" also matches atom:link.
		{`/rss/channel/link`, ns(ch+"/link[1]", ch+"/atom:link[1]")},
		{`/rss/channel/atom:link/@href`, ns(ch + "/atom:link[1]/@href")},
		// Predicates comparing child text.
		{`//item[dc:creator='Ada']/title`, ns(ch+"/item[1]/title[1]", ch+"/item[3]/title[1]")},
		{`//item[category='http']`, ns(ch + "/item[1]")},
		{`//item[category='api' and category='http']`, ns(ch + "/item[1]")},
		{`//item[category='api' or category='xml']/dc:creator`, ns(ch+"/item[1]/dc:creator[1]", ch+"/item[2]/dc:creator[1]")},
		{`//item[not(description)]/title`, ns(ch + "/item[3]/title[1]")},
		{`//item[starts-with(link, 'https://blog.example.com/x')]/title`, ns(ch + "/item[2]/title[1]")},
		{`//item[contains(title, 'API')]`, ns(ch + "/item[1]")},
		// Unions, parentheses, predicates on filter expressions.
		{`//item/title | /rss/channel/title`, ns(ch+"/title[1]", ch+"/item[1]/title[1]", ch+"/item[2]/title[1]", ch+"/item[3]/title[1]")},
		{`(//item/title | //item/link)[position() <= 2]`, ns(ch+"/item[1]/title[1]", ch+"/item[1]/link[1]")},
		{`(//item)[2]/title`, ns(ch + "/item[2]/title[1]")},
		// text(), processing instructions, entities, CDATA.
		{`//item[1]/description/text()`, ns(ch + "/item[1]/description[1]/text()[1]")},
		{`string(//item[1]/description)`, val("string:<p>Retries are <b>hard</b>.</p>")},
		{`string(/rss/channel/description)`, val("string:Posts about APIs & tooling")},
		{`/processing-instruction('xml-stylesheet')`, ns("/processing-instruction('xml-stylesheet')[1]")},
		{`/processing-instruction()`, ns("/processing-instruction('xml-stylesheet')[1]")},
		{`/processing-instruction('other')`, nil},
		{`string(/processing-instruction())`, val(`string:type="text/xsl" href="/rss.xsl"`)},
		// Scalars.
		{`count(//item)`, val("number:3")},
		{`count(//category)`, val("number:4")},
		{`normalize-space(//item[1]/title)`, val("string:Designing idempotent APIs")},
		{`string(/rss/@version)`, val("string:2.0")},
		{`/rss/@version = 2`, val("boolean:true")},
		{`name(//*[local-name()='creator'][1])`, val("string:dc:creator")},
		{`namespace-uri(//dc:creator)`, val("string:http://purl.org/dc/elements/1.1/")},
		{`local-name(/*)`, val("string:rss")},
	})
}

func TestSOAP11Fault(t *testing.T) {
	const fault = "/soap:Envelope[1]/soap:Body[1]/soap:Fault[1]"
	const of = fault + "/detail[1]/ns1:OrderFault[1]"
	const token = "/soap:Envelope[1]/soap:Header[1]/wsse:Security[1]/wsse:UsernameToken[1]"
	runCases(t, sample(t, "soap11-fault.xml"), Options{}, []queryCase{
		{`/soap:Envelope/soap:Body/soap:Fault/faultstring`, ns(fault + "/faultstring[1]")},
		{`boolean(//soap:Fault)`, val("boolean:true")},
		{`string(//faultcode)`, val("string:soap:Server")},
		{`//ns1:sku`, ns(of+"/ns1:sku[1]", of+"/ns1:sku[2]")},
		{`//sku[. = 'XYZ-9']`, ns(of + "/ns1:sku[2]")},
		{`//detail/*/*[2]`, ns(of + "/ns1:sku[1]")},
		{`//wsse:Security/@soap:mustUnderstand`, ns("/soap:Envelope[1]/soap:Header[1]/wsse:Security[1]/@soap:mustUnderstand")},
		{`//@wsu:Id`, ns(token + "/@wsu:Id")},
		{`//@Id`, nil}, // unprefixed attribute names match unprefixed attributes only
		{`normalize-space(//wsse:Username)`, val("string:svc-orders")},
		{`count(//*)`, val("number:14")},
		{`count(//@*)`, val("number:3")}, // xmlns declarations are not attributes
		{`//faultstring[lang('en')]`, ns(fault + "/faultstring[1]")},
		{`//faultstring[lang('EN')]`, ns(fault + "/faultstring[1]")},
		{`//faultcode[lang('en')]`, nil},
		{`//soap:Body//text()[normalize-space()]`, ns(fault+"/faultcode[1]/text()[1]", fault+"/faultstring[1]/text()[1]",
			of+"/ns1:code[1]/text()[1]", of+"/ns1:sku[1]/text()[1]", of+"/ns1:sku[2]/text()[1]")},

		// Axes.
		{`//ns1:sku[1]/following-sibling::*`, ns(of + "/ns1:sku[2]")},
		{`//ns1:sku[2]/preceding-sibling::*[1]`, ns(of + "/ns1:sku[1]")},
		{`//ns1:sku[2]/preceding-sibling::*[last()]`, ns(of + "/ns1:code[1]")},
		{`//ns1:code/ancestor::*`, ns("/soap:Envelope[1]", "/soap:Envelope[1]/soap:Body[1]", fault, fault+"/detail[1]", of)},
		{`//ns1:code/ancestor::*[2]`, ns(fault + "/detail[1]")},
		{`//ns1:code/ancestor-or-self::*[1]`, ns(of + "/ns1:code[1]")},
		{`//faultcode/following::*`, ns(fault+"/faultstring[1]", fault+"/detail[1]", of, of+"/ns1:code[1]", of+"/ns1:sku[1]", of+"/ns1:sku[2]")},
		{`//faultstring/preceding::*`, ns("/soap:Envelope[1]/soap:Header[1]", "/soap:Envelope[1]/soap:Header[1]/wsse:Security[1]", token, token+"/wsse:Username[1]", fault+"/faultcode[1]")},
		{`//faultstring/preceding::*[1]`, ns(fault + "/faultcode[1]")},
		{`//faultstring/@xml:lang/following::*[1]`, ns(fault + "/detail[1]")},
		{`//faultstring/@xml:lang/preceding::*[1]`, ns(fault + "/faultcode[1]")},
		{`//faultstring/@xml:lang/parent::*`, ns(fault + "/faultstring[1]")},
		{`//faultstring/@xml:lang/..`, ns(fault + "/faultstring[1]")},
		{`//faultstring/@xml:lang/following-sibling::node()`, nil},
		{`//soap:Fault/child::*[2]/self::faultstring`, ns(fault + "/faultstring[1]")},
		{`//soap:Fault/child::*[2]/self::faultcode`, nil},
		{`/descendant::ns1:OrderFault/descendant-or-self::*[1]`, ns(of)},
		{`count(//ns1:OrderFault/descendant-or-self::node())`, val("number:11")}, // with whitespace text
	})
}

func TestSOAP12Numbers(t *testing.T) {
	const resp = "/env:Envelope[1]/env:Body[1]/GetQuoteResponse[1]"
	runCases(t, sample(t, "soap12-response.xml"), Options{}, []queryCase{
		{`//GetQuoteResponse/Quote/@symbol`, ns(resp+"/Quote[1]/@symbol", resp+"/Quote[2]/@symbol", resp+"/Quote[3]/@symbol")},
		{`//Quote[Price > 100]/@symbol`, ns(resp + "/Quote[1]/@symbol")},
		{`//Quote[Price < 10]/@symbol`, ns(resp + "/Quote[2]/@symbol")}, // "n/a" is NaN
		{`//Quote[Change < 0]/@symbol`, ns(resp + "/Quote[1]/@symbol")},
		{`//Quote[number(Price) != number(Price)]/@symbol`, ns(resp + "/Quote[3]/@symbol")},
		{`//Quote[@currency='USD'][2]/@symbol`, ns(resp + "/Quote[3]/@symbol")},
		{`//Quote[2][@currency='USD']`, nil},
		{`//Quote[Volume = 0]/@symbol`, ns(resp + "/Quote[3]/@symbol")},
		{`//Quote[Change = '']/@symbol`, ns(resp + "/Quote[3]/@symbol")},
		{`//Quote[not(Change/text())]/@symbol`, ns(resp + "/Quote[3]/@symbol")},
		{`//Quote[Price != 'n/a']/@symbol`, ns(resp+"/Quote[1]/@symbol", resp+"/Quote[2]/@symbol")},
		{`//Quote/Price[. >= 9.5 and . <= 101.25]`, ns(resp+"/Quote[1]/Price[1]", resp+"/Quote[2]/Price[1]")},
		{`//Quote[Price = //Quote[@symbol='INIT']/Price]/@symbol`, ns(resp + "/Quote[2]/@symbol")},
		{`//Price[. = ../Volume]`, nil},
		{`count(//Quote[@currency='USD'])`, val("number:2")},
		{`sum(//Quote/Volume)`, val("number:1288300")},
		{`sum(//Price)`, val("number:NaN")},
		{`round(//Quote[1]/Price)`, val("number:101")},
		{`//Quote[1]/Price * 2`, val("number:202.5")},
		{`//Quote[1]/Change + //Quote[2]/Change`, val("number:-0.65")},
		{`string(//Quote[2]/Change)`, val("string:0.10")},
		{`number(//Quote[2]/Change)`, val("number:0.1")},
		{`//Quote/Volume > //Quote/Price`, val("boolean:true")},
		{`//Quote/Price > 1000`, val("boolean:false")},
		{`//Quote/@symbol = 'INIT'`, val("boolean:true")},
		{`//Quote/@symbol != 'INIT'`, val("boolean:true")},
		{`//Missing = 'x'`, val("boolean:false")},
		{`//Missing != 'x'`, val("boolean:false")},
		{`//Missing = false()`, val("boolean:true")},
		{`//Quote = true()`, val("boolean:true")},
	})
}

func TestPom(t *testing.T) {
	runCases(t, sample(t, "pom.xml"), Options{}, []queryCase{
		{`/project/artifactId`, ns("/project[1]/artifactId[1]")},
		{`string(/project/artifactId)`, val("string:orders-service")},
		{`string(/project/properties/java.version)`, val("string:21")},
		{`//dependency[scope='test']/artifactId`, ns("/project[1]/dependencies[1]/dependency[3]/artifactId[1]")},
		{`//dependency[not(scope)]/artifactId`, ns("/project[1]/dependencies[1]/dependency[1]/artifactId[1]")},
		{`//dependency[groupId='org.postgresql' or groupId='org.testcontainers'][artifactId='postgresql']`,
			ns("/project[1]/dependencies[1]/dependency[2]", "/project[1]/dependencies[1]/dependency[3]")},
		{`/project/dependencies/dependency[version]/version`, ns("/project[1]/dependencies[1]/dependency[3]/version[1]")},
		{`//dependency[last()]/scope/text()`, ns("/project[1]/dependencies[1]/dependency[3]/scope[1]/text()[1]")},
		{`count(/project/dependencies/dependency)`, val("number:3")},
		{`/project/parent/comment()`, ns("/project[1]/parent[1]/comment()[1]")},
		{`string(/project/parent/comment())`, val("string: lookup parent from repository ")},
		{`/project/parent/relativePath`, ns("/project[1]/parent[1]/relativePath[1]")},
		{`substring-before(/project/@xsi:schemaLocation, ' ')`, val("string:http://maven.apache.org/POM/4.0.0")},
		{`/project/@xsi:schemaLocation`, ns("/project[1]/@xsi:schemaLocation")},
		{`/project/version[contains(., 'SNAPSHOT')]`, ns("/project[1]/version[1]")},
		{`substring-after(/project/parent/version, '.')`, val("string:3.4")},
	})
}

func TestSitemap(t *testing.T) {
	runCases(t, sample(t, "sitemap.xml"), Options{}, []queryCase{
		{`//url[priority > 0.5]/loc`, ns("/urlset[1]/url[1]/loc[1]", "/urlset[1]/url[2]/loc[1]")},
		{`//url[not(lastmod)]/loc`, ns("/urlset[1]/url[3]/loc[1]")},
		{`string(//url[not(lastmod)]/loc)`, val("string:https://www.example.com/blog?page=2&sort=new")},
		{`//image:loc`, ns("/urlset[1]/url[2]/image:image[1]/image:loc[1]")},
		{`count(//loc)`, val("number:4")}, // lenient: image:loc too
		{`count(//url/loc)`, val("number:3")},
		{`count(//url[lastmod >= '2026-09-01'])`, val("number:0")}, // relational operators compare numbers
		{`//url[translate(lastmod, '-', '') >= 20260901]/loc`, ns("/urlset[1]/url[1]/loc[1]")},
		{`//url[substring(lastmod, 6, 2) = '08']/loc`, ns("/urlset[1]/url[2]/loc[1]")},
		{`//url[string-length(changefreq) > 0]`, ns("/urlset[1]/url[1]")},
		{`//priority[. = 1]`, ns("/urlset[1]/url[1]/priority[1]")},
		{`//priority[. = '1']`, nil},
		{`sum(//priority) div count(//priority)`, val("number:0.7000000000000001")},
	})
}

func TestXHTML(t *testing.T) {
	const body = "/html[1]/body[1]"
	const table = body + "/table[1]"
	runCases(t, sample(t, "xhtml.html"), Options{}, []queryCase{
		{`/html/head/title`, ns("/html[1]/head[1]/title[1]")},
		{`/node()`, ns("/html[1]")}, // the DOCTYPE is not an XPath node
		{`//tr[@class='line']/td[1]`, ns(table+"/tr[2]/td[1]", table+"/tr[4]/td[1]")},
		{`//tr[contains(concat(' ', normalize-space(@class), ' '), ' line ')]`, ns(table+"/tr[2]", table+"/tr[3]", table+"/tr[4]")},
		{`//table[@id='lines']/tr[position() > 1]/td[2]`, ns(table+"/tr[2]/td[2]", table+"/tr[3]/td[2]", table+"/tr[4]/td[2]")},
		{`sum(//tr[@class]/td[2])`, val("number:13")},
		{`//tr[td[3] > 1]/td[1]`, ns(table+"/tr[2]/td[1]", table+"/tr[3]/td[1]")},
		{`//p/text()`, ns(body+"/p[1]/text()[1]", body+"/p[1]/text()[2]", body+"/p[1]/text()[3]")},
		{`//p/br`, ns(body+"/p[1]/br[1]", body+"/p[1]/br[2]")},
		{`string(//p/text()[2])`, val("string:Jane Roe")},
		{`//h1//text()`, ns(body+"/h1[1]/text()[1]", body+"/h1[1]/span[1]/text()[1]")},
		{`string(//h1)`, val("string:Order #1042")},
		{`string(//a[starts-with(@href, '/orders')]/@href)`, val("string:/orders?id=1042&view=full")},
		{`//*[@id]`, ns(body+"/h1[1]", table)},
		{`//li[a='Help']/following-sibling::li/a/@href`, ns(body + "/ul[1]/li[2]/a[1]/@href")},
		{`//td[.='2']/preceding-sibling::td`, ns(table + "/tr[2]/td[1]")},
		{`//th[2]/following-sibling::th[1]`, ns(table + "/tr[1]/th[3]")},
		{`count(/descendant::td)`, val("number:9")},
		{`/child::html/child::body/child::h1/attribute::id`, ns(body + "/h1[1]/@id")},
		{`//span/parent::h1/@id`, ns(body + "/h1[1]/@id")},
		{`//span/ancestor::*[last()]`, ns("/html[1]")},
		{`//span/self::span`, ns(body + "/h1[1]/span[1]")},
		{`//span/self::div`, nil},
		{`//tr[2]/descendant-or-self::*`, ns(table+"/tr[2]", table+"/tr[2]/td[1]", table+"/tr[2]/td[2]", table+"/tr[2]/td[3]")},
		{`//comment()`, nil},
		{`string(//meta/@charset)`, val("string:utf-8")},
	})
}

func TestSpringConfig(t *testing.T) {
	const ds = "/beans[1]/bean[1]"
	runCases(t, sample(t, "spring-config.xml"), Options{}, []queryCase{
		{`/comment()`, ns("/comment()[1]")},
		{`//comment()`, ns("/comment()[1]", "/beans[1]/comment()[1]", "/beans[1]/bean[3]/comment()[1]")},
		{`//bean[@id='dataSource']/property[@name='connectionTestQuery']/value/text()`, ns(ds + "/property[3]/value[1]/text()[1]")},
		{`string(//property[@name='connectionTestQuery']/value)`, val("string:SELECT 1 WHERE 2 > 1 AND 'a' <> 'b'")},
		{`//bean[@lazy-init='true']/@class`, ns("/beans[1]/bean[3]/@class")},
		{`string(//bean[property/@name='maxAttempts']/@id)`, val("string:retryTemplate")},
		{`//context:property-placeholder/@location`, ns("/beans[1]/context:property-placeholder[1]/@location")},
		{`//processing-instruction('generator')`, ns("/beans[1]/processing-instruction('generator')[1]")},
		{`string(//processing-instruction('generator'))`, val("string:orders-codegen 2.1")},
		{`//bean[constructor-arg/@ref = //bean/@id]/@id`, ns("/beans[1]/bean[2]/@id")},
		{`string(//property[@value > 5]/@name)`, val("string:maximumPoolSize")},
		{`count(//bean/property)`, val("number:4")},
		{`//bean[count(property) > 1]/@id`, ns(ds + "/@id")},
		{`//@destroy-method/..`, ns(ds)},
		{`name(//bean[1]/@destroy-method)`, val("string:destroy-method")},
		{`//comment()[contains(., 'TODO')]/..`, ns("/beans[1]/bean[3]")},
		{`count(//bean[3]/node())`, val("number:5")},
		{`//bean[3]/node()[2]`, ns("/beans[1]/bean[3]/property[1]")},
		{`//bean[3]/text()[2]`, ns("/beans[1]/bean[3]/text()[2]")},
		{`//bean[3]/comment()/preceding-sibling::node()[1]`, ns("/beans[1]/bean[3]/text()[2]")},
	})
}

func TestAtomNamespaces(t *testing.T) {
	doc := sample(t, "atom.xml")
	const (
		atom  = "http://www.w3.org/2005/Atom"
		xhtml = "http://www.w3.org/1999/xhtml"
	)
	content := "/feed[1]/entry[1]/content[1]"
	runCases(t, doc, Options{}, []queryCase{
		{`/feed/entry[2]/link[@rel='enclosure']/@length`, ns("/feed[1]/entry[2]/link[2]/@length")},
		{`//content/div`, ns(content + "/div[1]")},
		{`//*[namespace-uri()='` + xhtml + `']`, ns(content+"/div[1]", content+"/div[1]/em[1]")},
		{`//@xml:lang`, ns("/feed[1]/@xml:lang", content+"/@xml:lang")},
		{`//em[lang('de')]`, ns(content + "/div[1]/em[1]")},
		{`//entry[2]/title[lang('en')]`, ns("/feed[1]/entry[2]/title[1]")},
		{`string(//entry[2]/title)`, val("string:Incident <resolved>")},
	})
	// Strict names: the default namespace must be bound.
	runCases(t, doc, Options{StrictNames: true}, []queryCase{
		{`/feed`, nil},
		{`//@xml:lang`, ns("/feed[1]/@xml:lang", content+"/@xml:lang")},
	})
	runCases(t, doc, Options{StrictNames: true, Namespaces: map[string]string{"": atom, "h": xhtml}}, []queryCase{
		{`/feed/entry[1]/content/div`, nil},
		{`/feed/entry[1]/content/h:div/h:em`, ns(content + "/div[1]/em[1]")},
		{`count(//entry)`, val("number:2")},
	})
	runCases(t, doc, Options{StrictNames: true, Namespaces: map[string]string{"a": atom}}, []queryCase{
		{`/a:feed/a:entry/a:title[@type='html']`, ns("/feed[1]/entry[2]/title[1]")},
		{`count(/a:feed/a:*)`, val("number:6")},
	})
}

func TestNamespaceMatching(t *testing.T) {
	doc := sample(t, "soap12-response.xml")
	const (
		soap12 = "http://www.w3.org/2003/05/soap-envelope"
		quote  = "http://example.com/stockquote"
	)
	const resp = "/env:Envelope[1]/env:Body[1]/GetQuoteResponse[1]"
	for _, c := range []struct {
		name string
		opts Options
		expr string
		want []string
	}{
		{"lenient prefix as written", Options{}, `//env:Body`, ns("/env:Envelope[1]/env:Body[1]")},
		{"lenient other prefix", Options{}, `//soap:Body`, nil},
		{"lenient mapped prefix", Options{Namespaces: map[string]string{"soap": soap12}}, `//soap:Body`, ns("/env:Envelope[1]/env:Body[1]")},
		{"map wins over document prefix", Options{Namespaces: map[string]string{"env": "urn:wrong"}}, `//env:Body`, nil},
		{"lenient default namespace", Options{}, `count(//Quote)`, val("number:3")},
		{"strict default namespace", Options{StrictNames: true}, `count(//Quote)`, val("number:0")},
		{"strict bound default", Options{StrictNames: true, Namespaces: map[string]string{"": quote}}, `count(//Quote)`, val("number:3")},
		{"lenient bound default", Options{Namespaces: map[string]string{"": "urn:other"}}, `count(//Quote)`, val("number:0")},
		{"strict prefix", Options{StrictNames: true, Namespaces: map[string]string{"q": quote, "s": soap12}},
			`/s:Envelope/s:Body/q:GetQuoteResponse/q:Quote[@symbol='INIT']/q:Price`, ns(resp + "/Quote[2]/Price[1]")},
		{"strict prefix wildcard", Options{StrictNames: true, Namespaces: map[string]string{"q": quote}}, `count(//q:*)`, val("number:13")},
		{"lenient prefix wildcard", Options{}, `count(//env:*)`, val("number:2")},
		{"local-name and namespace-uri", Options{StrictNames: true},
			`count(//*[local-name()='Quote' and namespace-uri()='` + quote + `'])`, val("number:3")},
	} {
		t.Run(c.name, func(t *testing.T) {
			runCases(t, doc, c.opts, []queryCase{{c.expr, c.want}})
		})
	}
}
