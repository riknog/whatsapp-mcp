// Package identity turns WhatsApp JIDs into opaque contact refs and resolves
// the names or refs the model sends into one chat target.
//
//   - Refs: "c_" + HMAC-SHA256(ref.key, canonical JID), base32, 10 characters.
//     The model sees refs and names, never JIDs or phone numbers.
//   - DisplayName, Normalize and Ago: display name (design §5), accent-free
//     matching keys, and "há 5 min"-style relative times in pt-BR.
//   - Resolver: name or contact_ref to a Target, with the errors of docs/02-TOOLS.md
//     (phone_not_allowed, contact_not_found, ambiguous_contact, chat_hidden).
//     It depends on the small ContactSource interface. StoreSource adapts *store.Store.
//
// Target.JID is internal. It is for the send queue and must not reach tool
// output, errors or logs.
package identity
