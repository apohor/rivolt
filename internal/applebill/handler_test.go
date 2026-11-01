package applebill

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	testBundle = "dev.example.test"
	testProd   = "pro.year"
)

func testConfig() *Config {
	return &Config{BundleID: testBundle, Products: map[string]bool{testProd: true}, Environment: EnvProduction}
}

// ti is a transaction as Apple signs it, for tests.
func ti(orig string, expires time.Time, token string) TransactionInfo {
	return TransactionInfo{OriginalTransactionID: orig, TransactionID: orig + "-t", BundleID: testBundle, ProductID: testProd, Type: "Auto-Renewable Subscription",
		Environment: EnvProduction, AppAccountToken: token, PurchaseDate: time.Now().Add(-time.Hour).UnixMilli(), ExpiresDate: expires.UnixMilli(), SignedDate: time.Now().UnixMilli()}
}

func notification(c *testChain, t testing.TB, typ, sub, id string, signed time.Time, tx TransactionInfo) string {
	t.Helper()
	data := map[string]any{"environment": tx.Environment, "bundleId": tx.BundleID}
	if tx.OriginalTransactionID != "" {
		data["signedTransactionInfo"] = c.sign(t, tx)
	}
	body, _ := json.Marshal(map[string]string{"signedPayload": c.sign(t, map[string]any{
		"notificationType": typ, "subtype": sub, "notificationUUID": id, "version": "2.0", "signedDate": signed.UnixMilli(), "data": data})})
	return string(body)
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/notifications", strings.NewReader(body)))
	return rec
}

// Everything that must be refused is refused before the database is touched (the handler here has none).
func TestNotificationRejections(t *testing.T) {
	c := newChain(t, chainOpts{})
	var outcomes []string
	h := &Handler{Verifier: c.verifier, Config: testConfig(), Observe: func(o string) { outcomes = append(outcomes, o) }}
	exp := time.Now().Add(30 * 24 * time.Hour)
	tok := uuid.NewString()

	other := newChain(t, chainOpts{})
	wrongBundle := ti("rv-test-orig-1", exp, tok)
	wrongBundle.BundleID = "dev.other.test"
	wrongEnv := ti("rv-test-orig-1", exp, tok)
	wrongEnv.Environment = EnvSandbox
	wrongProd := ti("rv-test-orig-1", exp, tok)
	wrongProd.ProductID = "pro.other"
	noProd := ti("rv-test-orig-1", exp, tok)
	noProd.ProductID = ""
	for name, body := range map[string]string{
		"not json":             "nope",
		"no signed payload":    `{}`,
		"signed by not Apple":  notification(other, t, "DID_RENEW", "", uuid.NewString(), time.Now(), ti("rv-test-orig-1", exp, tok)),
		"another bundle":       notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), wrongBundle),
		"another environment":  notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), wrongEnv),
		"another product":      notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), wrongProd),
		"no product id":        notification(c, t, "DID_RENEW", "", uuid.NewString(), time.Now(), noProd),
		"no notification uuid": notification(c, t, "DID_RENEW", "", "", time.Now(), ti("rv-test-orig-1", exp, tok)),
	} {
		outcomes = nil
		if rec := post(h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		} else if len(outcomes) != 1 || outcomes[0] != OutcomeRejected {
			t.Errorf("%s: outcomes %v, want [rejected]", name, outcomes)
		}
	}
	// A transaction inside a genuine notification that is not Apple-signed is refused too.
	forged := map[string]any{"environment": EnvProduction, "bundleId": testBundle, "signedTransactionInfo": other.sign(t, ti("rv-test-orig-1", exp, tok))}
	b, _ := json.Marshal(map[string]string{"signedPayload": c.sign(t, map[string]any{"notificationType": "DID_RENEW", "notificationUUID": uuid.NewString(), "signedDate": time.Now().UnixMilli(), "data": forged})})
	if rec := post(h, string(b)); rec.Code != http.StatusBadRequest {
		t.Errorf("forged inner transaction: %d", rec.Code)
	}
}

// Apple's documented shapes: the field names the decoder reads.
func TestPayloadShapes(t *testing.T) {
	const payload = `{"notificationType":"DID_RENEW","subtype":"","notificationUUID":"002e14d5-51f5-4503-b5a8-c3a1af68eb20","version":"2.0","signedDate":1698148900000,
		"data":{"appAppleId":41234,"bundleId":"dev.example.test","bundleVersion":"1","environment":"Sandbox","signedTransactionInfo":"x","signedRenewalInfo":"y","status":1}}`
	var p NotificationPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p.NotificationType != "DID_RENEW" || p.NotificationUUID == "" || p.SignedDate != 1698148900000 ||
		p.Data == nil || p.Data.BundleID != "dev.example.test" || p.Data.Environment != "Sandbox" || p.Data.SignedTransactionInfo != "x" {
		t.Errorf("%v %+v", err, p)
	}
	const tx = `{"transactionId":"2000000000000002","originalTransactionId":"2000000000000001","bundleId":"dev.example.test","productId":"pro.year","purchaseDate":1698148800000,
		"originalPurchaseDate":1698148800000,"expiresDate":1729684800000,"quantity":1,"type":"Auto-Renewable Subscription","appAccountToken":"7e3fb20b-4cdb-47cc-936d-99d65f608138",
		"inAppOwnershipType":"PURCHASED","signedDate":1698148900000,"environment":"Sandbox","revocationDate":1698149000000,"revocationReason":1}`
	var ti TransactionInfo
	if err := json.Unmarshal([]byte(tx), &ti); err != nil || ti.OriginalTransactionID != "2000000000000001" || ti.AppAccountToken != "7e3fb20b-4cdb-47cc-936d-99d65f608138" ||
		!ti.Revoked() || ti.Expires().Year() != 2024 || ti.RevocationReason != 1 || !ti.Subscription() {
		t.Errorf("%v %+v", err, ti)
	}
}

func TestTransactionRejectionsBeforeTheDatabase(t *testing.T) {
	c := newChain(t, chainOpts{})
	uid := uuid.New()
	h := &Transaction{Verifier: c.verifier, Config: testConfig()}
	exp := time.Now().Add(30 * 24 * time.Hour)
	call := func(signed string) int {
		body, _ := json.Marshal(map[string]string{"signedTransaction": signed})
		rec := httptest.NewRecorder()
		h.Serve(uid, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(body))))
		return rec.Code
	}
	noToken := ti("rv-test-orig-1", exp, "")
	otherToken := ti("rv-test-orig-1", exp, uuid.NewString())
	wrongBundle := ti("rv-test-orig-1", exp, uid.String())
	wrongBundle.BundleID = "dev.other.test"
	noOrig := ti("", exp, uid.String())
	noProduct := ti("rv-test-orig-1", exp, uid.String()) // Config.Check skips an empty product (a notification without a transaction): the handler must not
	noProduct.ProductID = ""
	for name, signed := range map[string]string{
		"no appAccountToken (a transaction is a shareable blob)": c.sign(t, noToken),
		"another account's token":                                c.sign(t, otherToken),
		"another bundle":                                         c.sign(t, wrongBundle),
		"no original transaction id":                             c.sign(t, noOrig),
		"no product id":                                          c.sign(t, noProduct),
		"not Apple-signed":                                       newChain(t, chainOpts{}).sign(t, ti("rv-test-orig-1", exp, uid.String())),
		"garbage":                                                "abc",
	} {
		if code := call(signed); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	rec := httptest.NewRecorder()
	h.Serve(uid, rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed body: %d", rec.Code)
	}
}
