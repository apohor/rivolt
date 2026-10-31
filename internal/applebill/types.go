package applebill

import "time"

// NotificationBody is what Apple POSTs: one signed value.
type NotificationBody struct {
	SignedPayload string `json:"signedPayload"`
}

// NotificationPayload is the decoded signedPayload of an App Store Server Notification V2.
type NotificationPayload struct {
	NotificationType string            `json:"notificationType"`
	Subtype          string            `json:"subtype"`
	NotificationUUID string            `json:"notificationUUID"`
	Version          string            `json:"version"`
	SignedDate       int64             `json:"signedDate"` // ms since the epoch
	Data             *NotificationData `json:"data"`
}

// NotificationData is a payload's data object (absent on a TEST notification).
type NotificationData struct {
	Environment           string `json:"environment"`
	BundleID              string `json:"bundleId"`
	SignedTransactionInfo string `json:"signedTransactionInfo"`
	SignedRenewalInfo     string `json:"signedRenewalInfo"`
}

// TransactionInfo is a decoded signed transaction (JWSTransactionDecodedPayload), the fields Rivolt reads.
type TransactionInfo struct {
	OriginalTransactionID string `json:"originalTransactionId"`
	TransactionID         string `json:"transactionId"`
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	Type                  string `json:"type"`
	Environment           string `json:"environment"`
	AppAccountToken       string `json:"appAccountToken"`
	PurchaseDate          int64  `json:"purchaseDate"`
	ExpiresDate           int64  `json:"expiresDate"`
	RevocationDate        int64  `json:"revocationDate"`
	RevocationReason      int    `json:"revocationReason"`
	SignedDate            int64  `json:"signedDate"`
}

func ms(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// Expires is the end of the paid period (zero: none).
func (t TransactionInfo) Expires() time.Time { return ms(t.ExpiresDate) }

// Revoked is when Apple revoked the transaction (a refund, or a family-sharing loss); zero when it has not.
func (t TransactionInfo) Revoked() bool { return t.RevocationDate > 0 }

// Subscription is whether this transaction is an auto-renewable subscription, the only kind Rivolt Pro sells.
func (t TransactionInfo) Subscription() bool {
	return t.Type == "" || t.Type == "Auto-Renewable Subscription"
}
