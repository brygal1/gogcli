package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"sync"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/people/v1"

	"github.com/steipete/gogcli/internal/ui"
)

type resolvedHeaderContact struct {
	Name             string   `json:"name,omitempty"`
	Email            string   `json:"email,omitempty"`
	ContactName      string   `json:"contactName,omitempty"`
	InGoogleContacts bool     `json:"inGoogleContacts"`
	MembershipGroups []string `json:"membershipGroups,omitempty"`
}

type messageHeaderContacts struct {
	From []resolvedHeaderContact `json:"from,omitempty"`
	To   []resolvedHeaderContact `json:"to,omitempty"`
	Cc   []resolvedHeaderContact `json:"cc,omitempty"`
	Bcc  []resolvedHeaderContact `json:"bcc,omitempty"`
}

type cachedContactLookup struct {
	ContactName      string
	InGoogleContacts bool
	MembershipGroups []string
}

var errGoogleContactNotFound = errors.New("google contact not found")

type gmailContactResolver struct {
	svc     *people.Service
	enabled bool

	mu               sync.Mutex
	cache            map[string]cachedContactLookup
	groupNames       map[string]string
	groupNamesLoaded bool
	warning          string
}

func newGmailContactResolver(ctx context.Context, account string) *gmailContactResolver {
	svc, err := newPeopleContactsService(ctx, account)
	if err != nil {
		return &gmailContactResolver{
			enabled: false,
			cache:   map[string]cachedContactLookup{},
			warning: fmt.Sprintf("Google Contacts enrichment unavailable: %v", err),
		}
	}
	return &gmailContactResolver{
		svc:     svc,
		enabled: true,
		cache:   map[string]cachedContactLookup{},
	}
}

func (r *gmailContactResolver) LookupHeader(ctx context.Context, raw string) []resolvedHeaderContact {
	addresses := parseHeaderAddresses(raw)
	if len(addresses) == 0 {
		return nil
	}
	out := make([]resolvedHeaderContact, 0, len(addresses))
	for _, addr := range addresses {
		if strings.TrimSpace(addr.Email) == "" {
			continue
		}
		out = append(out, r.lookupAddress(ctx, addr))
	}
	return out
}

func (r *gmailContactResolver) LookupMessageHeaders(ctx context.Context, payload *gmail.MessagePart) messageHeaderContacts {
	return messageHeaderContacts{
		From: r.LookupHeader(ctx, headerValue(payload, "From")),
		To:   r.LookupHeader(ctx, headerValue(payload, "To")),
		Cc:   r.LookupHeader(ctx, headerValue(payload, "Cc")),
		Bcc:  r.LookupHeader(ctx, headerValue(payload, "Bcc")),
	}
}

type headerAddress struct {
	Name  string
	Email string
}

func parseHeaderAddresses(raw string) []headerAddress {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if addrs, err := mail.ParseAddressList(raw); err == nil {
		out := make([]headerAddress, 0, len(addrs))
		for _, addr := range addrs {
			if addr == nil || strings.TrimSpace(addr.Address) == "" {
				continue
			}
			out = append(out, headerAddress{
				Name:  strings.TrimSpace(addr.Name),
				Email: strings.ToLower(strings.TrimSpace(addr.Address)),
			})
		}
		return out
	}

	parts := strings.Split(raw, ",")
	out := make([]headerAddress, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if addr, err := mail.ParseAddress(part); err == nil && addr != nil && strings.TrimSpace(addr.Address) != "" {
			out = append(out, headerAddress{
				Name:  strings.TrimSpace(addr.Name),
				Email: strings.ToLower(strings.TrimSpace(addr.Address)),
			})
			continue
		}
		email := fallbackHeaderEmail(part)
		if email == "" {
			continue
		}
		out = append(out, headerAddress{
			Name:  "",
			Email: email,
		})
	}
	return out
}

func fallbackHeaderEmail(part string) string {
	if start := strings.LastIndex(part, "<"); start != -1 {
		if end := strings.LastIndex(part, ">"); end > start {
			part = part[start+1 : end]
		}
	}
	part = strings.Trim(strings.TrimSpace(part), "\"")
	if strings.Contains(part, "@") {
		return strings.ToLower(part)
	}
	return ""
}

func (r *gmailContactResolver) lookupAddress(ctx context.Context, addr headerAddress) resolvedHeaderContact {
	email := strings.ToLower(strings.TrimSpace(addr.Email))
	result := resolvedHeaderContact{
		Name:             strings.TrimSpace(addr.Name),
		Email:            email,
		InGoogleContacts: false,
	}
	if email == "" {
		return result
	}

	r.mu.Lock()
	cached, ok := r.cache[email]
	r.mu.Unlock()
	if ok {
		return mergeResolvedHeaderContact(result, cached)
	}

	core := cachedContactLookup{}
	if r.enabled && r.svc != nil {
		person, err := findGoogleContactByEmail(ctx, r.svc, email)
		if err != nil && !errors.Is(err, errGoogleContactNotFound) {
			r.setWarningf("Google Contacts enrichment incomplete: contact lookup failed: %v", err)
		}
		if err == nil && person != nil {
			core.InGoogleContacts = true
			core.ContactName = primaryName(person)
			core.MembershipGroups = r.lookupMembershipGroups(person)
		}
	}

	r.mu.Lock()
	r.cache[email] = core
	r.mu.Unlock()

	return mergeResolvedHeaderContact(result, core)
}

func mergeResolvedHeaderContact(base resolvedHeaderContact, core cachedContactLookup) resolvedHeaderContact {
	if base.Name == "" {
		base.Name = core.ContactName
	}
	base.ContactName = core.ContactName
	base.InGoogleContacts = core.InGoogleContacts
	if len(core.MembershipGroups) > 0 {
		base.MembershipGroups = append([]string(nil), core.MembershipGroups...)
	}
	return base
}

func (r *gmailContactResolver) lookupMembershipGroups(person *people.Person) []string {
	groupNames, err := r.contactGroupNames()
	if err != nil {
		r.setWarningf("Google Contacts enrichment incomplete: contact groups unavailable: %v", err)
		return nil
	}
	return membershipGroupNames(person, groupNames)
}

func (r *gmailContactResolver) contactGroupNames() (map[string]string, error) {
	r.mu.Lock()
	if r.groupNamesLoaded {
		groupNames := r.groupNames
		r.mu.Unlock()
		return groupNames, nil
	}
	r.mu.Unlock()

	groupNames, err := listContactGroupNames(r.svc)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.groupNames = groupNames
	r.groupNamesLoaded = true
	r.mu.Unlock()
	return groupNames, nil
}

func (r *gmailContactResolver) Available() bool {
	return r.enabled && r.svc != nil
}

func (r *gmailContactResolver) Warning() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.warning
}

func (r *gmailContactResolver) setWarningf(format string, args ...any) {
	warning := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warning == "" {
		r.warning = warning
	}
}

func addContactEnrichmentStatus(payload map[string]any, resolver *gmailContactResolver) {
	payload["contactEnrichmentAvailable"] = resolver.Available()
	if warning := resolver.Warning(); warning != "" {
		payload["contactEnrichmentWarning"] = warning
	}
}

func warnContactEnrichment(u *ui.UI, resolver *gmailContactResolver) {
	if u == nil || resolver == nil {
		return
	}
	if warning := resolver.Warning(); warning != "" {
		u.Err().Printf("WARNING: %s", warning)
	}
}

func findGoogleContactByEmail(ctx context.Context, svc *people.Service, email string) (*people.Person, error) {
	resp, err := svc.People.SearchContacts().
		Query(email).
		PageSize(10).
		ReadMask(contactsGetReadMask).
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	for _, result := range resp.Results {
		if result == nil || result.Person == nil {
			continue
		}
		if personHasEmail(result.Person, email) {
			return result.Person, nil
		}
	}
	return nil, errGoogleContactNotFound
}

func personHasEmail(person *people.Person, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if person == nil || email == "" {
		return false
	}
	for _, addr := range person.EmailAddresses {
		if addr == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(addr.Value), email) {
			return true
		}
	}
	return false
}

func formatResolvedHeaderContacts(contacts []resolvedHeaderContact) string {
	if len(contacts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		display := contact.Email
		switch {
		case contact.Name != "" && contact.Email != "":
			display = fmt.Sprintf("%s <%s>", contact.Name, contact.Email)
		case contact.ContactName != "" && contact.Email != "":
			display = fmt.Sprintf("%s <%s>", contact.ContactName, contact.Email)
		case contact.Name != "":
			display = contact.Name
		case contact.ContactName != "":
			display = contact.ContactName
		}
		segments := []string{fmt.Sprintf("in_google_contacts=%t", contact.InGoogleContacts)}
		if contact.ContactName != "" && !strings.EqualFold(contact.ContactName, contact.Name) {
			segments = append(segments, "contact_name="+contact.ContactName)
		}
		if len(contact.MembershipGroups) > 0 {
			segments = append(segments, "membership_groups="+strings.Join(contact.MembershipGroups, ", "))
		}
		parts = append(parts, display+" ["+strings.Join(segments, "; ")+"]")
	}
	return strings.Join(parts, "; ")
}

func summarizeResolvedHeaderContactsForTable(contacts []resolvedHeaderContact) (string, string) {
	if len(contacts) == 0 {
		return "", ""
	}
	inContacts := "no"
	groups := map[string]struct{}{}
	for _, contact := range contacts {
		if contact.InGoogleContacts {
			inContacts = sendAsYes
		}
		for _, group := range contact.MembershipGroups {
			group = strings.TrimSpace(group)
			if group == "" {
				continue
			}
			groups[group] = struct{}{}
		}
	}
	groupList := make([]string, 0, len(groups))
	for group := range groups {
		groupList = append(groupList, group)
	}
	sort.Strings(groupList)
	return inContacts, strings.Join(groupList, ", ")
}

func summarizeSingleResolvedHeaderContactForTable(contact *resolvedHeaderContact) (string, string) {
	if contact == nil {
		return summarizeResolvedHeaderContactsForTable(nil)
	}
	return summarizeResolvedHeaderContactsForTable([]resolvedHeaderContact{*contact})
}

type headerContactField struct {
	Label   string
	Key     string
	Value   string
	Summary string
}

func buildHeaderContactFields(payload *gmail.MessagePart, contacts messageHeaderContacts) []headerContactField {
	return []headerContactField{
		{
			Label:   "From",
			Key:     "from",
			Value:   headerValue(payload, "From"),
			Summary: formatResolvedHeaderContacts(contacts.From),
		},
		{
			Label:   "To",
			Key:     "to",
			Value:   headerValue(payload, "To"),
			Summary: formatResolvedHeaderContacts(contacts.To),
		},
		{
			Label:   "Cc",
			Key:     "cc",
			Value:   headerValue(payload, "Cc"),
			Summary: formatResolvedHeaderContacts(contacts.Cc),
		},
		{
			Label:   "Bcc",
			Key:     "bcc",
			Value:   headerValue(payload, "Bcc"),
			Summary: formatResolvedHeaderContacts(contacts.Bcc),
		},
	}
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && strings.Contains(value, "@")
}
