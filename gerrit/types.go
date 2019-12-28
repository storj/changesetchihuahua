package gerrit

import (
	"strconv"
	"time"
)

// These types are based on the entity definitions at
// https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html ,
// https://gerrit-review.googlesource.com/Documentation/rest-api-accounts.html , etc.
// Note that although they represent much the same things, these types are not the same as the
// types used for streaming events, which can be found in gerrit/events/types.go. These two
// classes of type are, for the most part, wholly incompatible with each other.

// AccountInfo contains information about a Gerrit account.
type AccountInfo struct {
	// AccountID is the numeric ID of the account.
	AccountID int `json:"_account_id,omitempty"`
	// Name is the full name of the user. Only set if detailed account information is requested
	// with DescribeDetailedAccounts (for change queries) or DescribeDetails (for account
	// queries).
	Name string
	// Email is the email address the user prefers to be contacted through. Only set if detailed
	// account information is requested with DescribeDetailedAccounts (for change queries) or
	// DescribeDetails (for account queries).
	Email string
	// SecondaryEmails is a list of the secondary email addresses of the user. Only set for
	// account queries when DescribeAllEmails is given, and if the calling user has the
	// ModifyAccount capability.
	SecondaryEmails []string `json:",omitempty"`
	// Username is the username of the user. Only set if detailed account information is
	// requested with DescribeDetailedAccounts (for change queries) or DescribeDetails (for
	// account queries).
	Username string
	// Avatars is a list of usable avatar icons for the user.
	Avatars []struct {
		URL    string
		Height int
	} `json:",omitempty"`

	// MoreAccounts indicates whether an account query would deliver more results if not
	// limited. Only set on the last account that is returned.
	MoreAccounts bool `json:"_more_accounts,omitempty"`
}

func (ai *AccountInfo) String() string {
	str := ai.Username
	if ai.Name != "" {
		str += " (" + ai.Name + ")"
	} else if ai.Email != "" {
		str += " (" + ai.Email + ")"
	}
	return str
}

// LabelInfo contains information about a label on a change, always corresponding to the
// current patch set.
type LabelInfo struct {
	// Optional is whether the label is optional. Optional means the label may be set, but
	// it's neither necessary for submission nor does it block submission if set.
	Optional bool

	// ----------------------------------------------------------------------------
	// The following fields are only set when DescribeLabels is requested.
	// ----------------------------------------------------------------------------

	// Approved is one user who approved this label on the change (voted the maximum value).
	Approved *AccountInfo
	// Rejected is one user who rejected this label on the change (voted the minimum value).
	Rejected *AccountInfo
	// Recommended is one user who recommended this label on the change (voted positively, but
	// not the maximum value).
	Recommended *AccountInfo
	// Disliked is one user who disliked this label on the change (voted negatively, but not
	// the minimum value).
	Disliked *AccountInfo
	// Blocking is whether the labels blocks submit operation.
	Blocking bool
	// Value is the voting value of the user who recommended/disliked this label on the change
	// if it is not "+1"/"-1".
	Value string
	// DefaultValue is the default voting value for the label. This value may be outside the
	// range specified in PermittedLabels.
	DefaultValue string

	// ----------------------------------------------------------------------------
	// The following fields are only set when DescribeDetailedLabels is requested.
	// ----------------------------------------------------------------------------

	// All is a list of all approvals for this label as a list of ApprovalInfo entities. Items
	// in this list may not represent actual votes cast by users; if a user votes on any label,
	// a corresponding ApprovalInfo will appear in this list for all labels.
	All []ApprovalInfo
	// Values is a map of all values that are allowed for this label. The map maps the values
	// ("-2","-1","0","+1","+2") to the value descriptions.
	Values map[string]string
}

type ApprovalInfo struct {
	AccountInfo

	// Value is the vote that the user has given for the label. If present and zero, the user
	// is permitted to vote on the label. If absent, the user is not permitted to vote on that
	// label.
	Value *int
	// PermittedVotingRange is the VotingRangeInfo the user is authorized to vote on that
	// label. If present, the user is permitted to vote on the label regarding the range
	// values. If absent, the user is not permitted to vote on that label.
	PermittedVotingRange VotingRangeInfo
	// Date is the time and date describing when the approval was made.
	Date string
	// Tag is the value of the tag field from ReviewInput set while posting the review.
	// Votes/comments that contain tag with 'autogenerated:' prefix can be filtered out in the
	// web UI. NOTE: To apply different tags on different votes/comments multiple invocations
	// of the REST call are required.
	Tag string
	// PostSubmit indicates that this vote was made after the change was submitted.
	PostSubmit bool `json:",omitempty"`
}

// VotingRangeInfo describes the continuous voting range from min to max values.
type VotingRangeInfo struct {
	// Min is the minimum voting value.
	Min int
	// Max is the maximum voting value.
	Max int
}

// ChangeInfo refers to a change being reviewed, or that was already reviewed.
type ChangeInfo struct {
	// ID gives the ID of the change in the format "<project>~<branch>~<Change-Id>", where
	// 'project', 'branch', and 'Change-Id' are URL encoded. For 'branch' the refs/heads/
	// prefix is omitted.
	ID string
	// Project is the name of the project.
	Project string
	// Branch is the name of the target branch. The refs/heads/ prefix is omitted.
	Branch string
	// Topic is the topic to which this change belongs.
	Topic string
	// Assignee is the assignee of the change.
	Assignee AccountInfo
	// Hashtags is a list of hashtags that are set on the change (only populated when NoteDb is
	// enabled).
	Hashtags []string
	// ChangeID is the Change-ID of the change.
	ChangeID string
	// Subject is the subject of the change (header line of the commit message).
	Subject string
	// Status is the status of the change ("NEW"/"MERGED"/"ABANDONED").
	Status string
	// Created is the timestamp of when the change was created.
	Created string
	// Updated is the timestamp of when the change was last updated.
	Updated string
	// Submitted is the timestamp of when the change was submitted.
	Submitted string
	// Submitter is the user who submitted the change.
	Submitter AccountInfo
	// Starred indicates whether the calling user has starred this change with the default label.
	Starred bool `json:",omitempty"`
	// Stars is a list of star labels that are applied by the calling user to this change. The
	// labels are lexicographically sorted.
	Stars []string
	// Reviewed indicates whether the change was reviewed by the calling user. Only set if
	// DescribeReviewed is requested.
	Reviewed bool `json:",omitempty"`
	// SubmitType is the submit type of the change ("INHERIT"/"FAST_FORWARD_ONLY"/
	// "MERGE_IF_NECESSARY"/"ALWAYS_MERGE"/"CHERRY_PICK"/"REBASE_IF_NECESSARY"/"REBASE_ALWAYS").
	// Not set for merged changes.
	// Mergeable indicates whether the change is mergeable. Not set for merged changes, if the
	// change has not yet been tested, or if DescribeSkipMergeable is passed or when
	// change.api.excludeMergeableInChangeInfo is set in the Gerrit config.
	Mergeable bool
	// Submittable is whether the change has been approved by the project submit rules. Only
	// set if requested with DescribeSubmittable.
	Submittable bool
	// Insertions is the number of inserted lines.
	Insertions int
	// Deletions is the number of deleted lines.
	Deletions int
	// TotalCommentCount is the total number of inline comments across all patch sets. Not set
	// if the current change index doesn't have the data.
	TotalCommentCount int
	// UnresolvedCommentCount is the number of unresolved inline comment threads across all
	// patch sets. Not set if the current change index doesn't have the data.
	UnresolvedCommentCount int
	// Number is the legacy numeric ID of the change.
	Number int `json:"_number,omitempty"`
	// Owner is the owner of the change.
	Owner AccountInfo
	// Actions is actions the caller might be able to perform on this revision. The information
	// is a map of view name to ActionInfo entries.
	Actions map[string]ActionInfo
	// Requirements is a list of the requirements to be met before this change can be submitted.
	Requirements []Requirement
	// Labels is the labels of the change as a map that maps the label names to LabelInfo
	// entries. Only set if DescribeLabels or DescribeDetailedLabels are requested.
	Labels map[string]LabelInfo
	// PermittedLabels is a map of the permitted labels that maps a label name to the list of
	// values that are allowed for that label. Only set if DescribeDetailedLabels is requested.
	PermittedLabels map[string][]string
	// RemovableReviewers is the reviewers that can be removed by the calling user as a list of
	// AccountInfo entities. Only set if DescribeDetailedLabels is requested.
	RemovableReviewers []AccountInfo
	// Reviewers is a map that maps a reviewer state to a list of AccountInfo entities. Possible
	// reviewer states are "REVIEWER", "CC", and "REMOVED". Only set if DescribeDetailedLabels
	// is requested.
	Reviewers map[string]AccountInfo
	// PendingReviewers is updates to Reviewers that have been made while the change was in the
	// WIP state. Only present on WIP changes and only if there are pending reviewer updates to
	// report. These are reviewers who have not yet been notified about being added to or
	// removed from the change.
	PendingReviewers map[string]AccountInfo
	// ReviewerUpdates is updates to Reviewers set for the change as ReviewerUpdateInfo
	// entities. Only set if DescribeReviewerUpdates is requested and if NoteDb is enabled.
	ReviewerUpdates []ReviewerUpdateInfo
	// Messages is messages associated with the change as a list of ChangeMessageInfo
	// entities. Only set if DescribeMessages is requested.
	Messages []ChangeMessageInfo
	// CurrentRevision is the commit ID of the current patch set of this change. Only set if
	// DescribeCurrentRevision or DescribeAllRevisions are requested.
	CurrentRevision string
	// Revisions is all patch sets of this change as a map that maps the commit ID of the
	// patch set to a RevisionInfo entity. Only set if DescribeCurrentRevision is requested
	// (in which case it will only contain a key for the current revision) or if
	// DescribeAllRevisions is requested.
	Revisions map[string]RevisionInfo
	// TrackingIDs is a list of TrackingIDInfo entities describing references to external
	// tracking systems. Only set if DescribeTrackingIDs is requested.
	TrackingIDs []TrackingIDInfo `json:"tracking_ids"`
	// MoreChanges indicates whether the query would deliver more results if not limited.
	// Only set on the last change that is returned.
	MoreChanges bool `json:"_more_changes,omitempty"`
	// Problems is a list of ProblemInfo entities describing potential problems with this
	// change. Only set if DescribeCheck is requested.
	Problems []ProblemInfo
	// IsPrivate indicates whether the change is marked as private.
	IsPrivate bool
	// WorkInProgress indicates whether the change is marked as Work In Progress.
	WorkInProgress bool
	// HasReviewStarted indicates whether the change has been marked Ready at some point in
	// time.
	HasReviewStarted bool
	// RevertOf gives the numeric Change-Id of the change that this change reverts.
	RevertOf string `json:",omitempty"`
}

// ActionInfo describes a REST API call the client can make to manipulate a resource. These are
// frequently implemented by plugins and may be discovered at runtime.
type ActionInfo struct {
	// Method is the HTTP method to use with the action. Most actions use POST, PUT or DELETE
	// to cause state changes.
	Method string
	// Label is a short title to display to a user describing the action. In the Gerrit web
	// interface the label is used as the text on the button presented in the UI.
	Label string
	// Title is longer text to display describing the action. In a web UI this should be the
	// title attribute of the element, displaying when the user hovers the mouse.
	Title string
	// Enabled indicates that the action is permitted at this time and the caller is likely
	// allowed to execute it. This may change if state is updated at the server or permissions
	// are modified.
	Enabled bool
}

// Requirement contains information about a requirement relative to a change.
type Requirement struct {
	// Status is the status of the requirement. Can be either "OK", "NOT_READY" or "RULE_ERROR".
	Status string
	// FallbackText is a human readable reason.
	FallbackText string
	// Type is an alphanumerical (plus hyphens or underscores) string to identify what the
	// requirement is and why it was triggered. Can be seen as a class: requirements sharing
	// the same type were created for a similar reason, and the data structure will follow one
	// set of rules.
	Type string
	// Data holds custom key-value strings, used in templates to render richer status messages.
	// (Not sure what structure that data takes.)
	Data interface{}
}

// ReviewerUpdateInfo contains information about updates to change’s reviewers set.
type ReviewerUpdateInfo struct {
	// Updated is the Timestamp of the update.
	Updated string
	// UpdatedBy is the account which modified state of the reviewer in question as AccountInfo
	// entity.
	UpdatedBy AccountInfo
	// Reviewer is the reviewer account added or removed from the change as an AccountInfo
	// entity.
	Reviewer *AccountInfo
	// State is the reviewer state, one of "REVIEWER", "CC" or "REMOVED".
	State string
}

// ChangeMessageInfo contains information about a message attached to a change.
type ChangeMessageInfo struct {
	// ID is the ID of the message.
	ID string
	// Author is the author of the message as an AccountInfo entity. Unset if written by the
	// Gerrit system.
	Author AccountInfo
	// RealAuthor is the real author of the message as an AccountInfo entity. Set if the
	// message was posted on behalf of another user.
	RealAuthor *AccountInfo
	// Date is the timestamp this message was posted.
	Date string
	// Message is the text left by the user.
	Message string
	// Tag is the value of the tag field from ReviewInput set while posting the review.
	// Votes/comments that contain tag with 'autogenerated:' prefix can be filtered out in the
	// web UI. NOTE: To apply different tags on different votes/comments multiple invocations
	// of the REST call are required.
	Tag string
	// RevisionNumber indicates which patchset (if any) generated this message.
	RevisionNumber int `json:"_revision_number"`
}

// RevisionInfo contains information about a patch set. Not all fields are returned by default.
// Additional fields can be obtained by settings fields on QueryChangesOpts.
type RevisionInfo struct {
	// Kind is the change kind. Valid values are "REWORK", "TRIVIAL_REBASE",
	// "MERGE_FIRST_PARENT_UPDATE", "NO_CODE_CHANGE", and "NO_CHANGE".
	Kind string
	// Number is the patch set number, or "edit" if the patch set is an edit.
	Number PatchSetNumber `json:"_number"`
	// Created is the timestamp of when the patch set was created.
	Created string
	// Uploader is the uploader of the patch set as an AccountInfo entity.
	Uploader AccountInfo
	// Ref is the Git reference for the patch set.
	Ref string
	// Fetch is information about how to fetch this patch set. The fetch information is
	// provided as a map that maps the protocol name (“git”, “http”, “ssh”) to FetchInfo
	// entities. This information is only included if a plugin implementing the download
	// commands interface is installed.
	Fetch map[string]FetchInfo
	// Commit is the commit of the patch set as CommitInfo entity.
	Commit CommitInfo
	// Files is the files of the patch set as a map that maps the file names to FileInfo
	// entities. Only set if DescribeCurrentFiles or DescribeAllFiles options are requested.
	Files map[string]FileInfo
	// Actions is actions the caller might be able to perform on this revision. The information
	// is a map of view name to ActionInfo entities.
	Actions map[string]ActionInfo
	// Reviewed indicates whether the caller is authenticated and has commented on the
	// current revision. Only set if DescribeReviewed option is requested.
	Reviewed bool
	// MessageWithFooter contains the full commit message with Gerrit-specific commit footers,
	// as if this revision were submitted using the Cherry Pick submit type. Only set when
	// the DescribeCommitFooters option is requested and when this is the current patch set.
	MessageWithFooter string
	// PushCertificate contains the push certificate provided by the user when uploading this
	// patch set as a PushCertificateInfo entity. This field is set if and only if the
	// DescribePushCertificates option is requested; if no push certificate was provided, it
	// is set to an empty object.
	PushCertificate *PushCertificateInfo
	// Description is the description of this patchset, as displayed in the patchset selector
	// menu. May be empty if no description is set.
	Description string
}

// TrackingIDInfo describes a reference to an external tracking system.
type TrackingIDInfo struct {
	// System is the name of the external tracking system.
	System string
	// ID is the tracking id.
	ID string
}

// ProblemInfo contains a description of a potential consistency problem with a change. These are
// not related to the code review process, but rather indicate some inconsistency in Gerrit’s
// database or repository metadata related to the enclosing change.
type ProblemInfo struct {
	// Message is a plaintext message describing the problem with the change.
	Message string
	// Status is the status of fixing the problem ("FIXED", "FIX_FAILED"). Only set if a fix
	// was attempted.
	Status string
	// Outcome is an additional plaintext message describing the outcome of the fix, if Status
	// is set.
	Outcome string
}

// FetchInfo contains information about how to fetch a patch set via a certain protocol.
type FetchInfo struct {
	// URL is the URL of the project.
	URL string
	// Ref is the ref of the patch set.
	Ref string
	// Commands gives the download commands for this patch set as a map that maps the command
	// names to the commands. Only set if DescribeDownloadCommands is requested.
	Commands map[string]string
}

// CommitInfo contains information about a commit.
type CommitInfo struct {
	// Commit is the commit ID. Not set if included in a RevisionInfo entity that is contained
	// in a map which has the commit ID as key.
	Commit string
	// Parents is the parent commits of this commit as a list of CommitInfo entities. In each
	// parent only the commit and subject fields are populated.
	Parents []CommitInfo
	// Author is the author of the commit as a GitPersonInfo entity.
	Author GitPersonInfo
	// Committer is the committer of the commit as a GitPersonInfo entity.
	Committer GitPersonInfo
	// Subject is the subject of the commit (header line of the commit message).
	Subject string
	// Message is the commit message.
	Message string
	// WebLinks is links to the commit in external sites as a list of WebLinkInfo entities.
	WebLinks []WebLinkInfo
}

// The FileInfo entity contains information about a file in a patch set.
type FileInfo struct {
	// Status is the status of the file (“A”=Added, “D”=Deleted, “R”=Renamed, “C”=Copied,
	// “W”=Rewritten). Not set if the file was Modified (“M”).
	Status string
	// Binary indicates Whether the file is binary.
	Binary bool
	// OldPath is the old file path. Only set if the file was renamed or copied.
	OldPath string
	// LinesInserted is the number of inserted lines. Not set for binary files or if no lines
	// were inserted. An empty last line is not included in the count and hence this number can
	// differ by one from details provided in DiffInfo.
	LinesInserted int
	// LinesDeleted is the number of deleted lines. Not set for binary files or if no lines
	// were deleted. An empty last line is not included in the count and hence this number can
	// differ by one from details provided in DiffInfo.
	LinesDeleted int
	// SizeDelta is the number of bytes by which the file size increased/decreased.
	SizeDelta int
	// Size is the file size in bytes.
	Size int
}

// PushCertificateInfo contains information about a push certificate provided when the user pushed
// for review with git push --signed HEAD:refs/for/<branch>. Only used when signed push is enabled
// on the server.
type PushCertificateInfo struct {
	// Certificate is the signed certificate payload and GPG signature block.
	Certificate string
	// Key is information about the key that signed the push, along with any problems found
	// while checking the signature or the key itself, as a GpgKeyInfo entity.
	Key GpgKeyInfo
}

// GitPersonInfo contains information about the author/committer of a commit.
type GitPersonInfo struct {
	// Name is the name of the author/committer.
	Name string
	// Email is the email address of the author/committer.
	Email string
	// Date is the timestamp of when this identity was constructed.
	Date string
	// TZ is the timezone offset from UTC of when this identity was constructed.
	TZ string
}

// WebLinkInfo describes a link to an external site.
type WebLinkInfo struct {
	// Name is the link name.
	Name string
	// URL is the link URL.
	URL string
	// ImageURL is the URL to the icon of the link.
	ImageURL string
}

// GpgKeyInfo contains information about a GPG public key.
type GpgKeyInfo struct {
	// ID is the 8-char hex GPG key ID. Not set in map context.
	ID string
	// Fingerprint is the 40-char (plus spaces) hex GPG key fingerprint. Not set for deleted
	// keys.
	Fingerprint string
	// UserIDs is a list of OpenPGP User IDs associated with the public key. Not set for
	// deleted keys.
	UserIDs []string
	// Key is ASCII armored public key material. Not set for deleted keys.
	Key string
	// Status is the result of server-side checks on the key; one of "BAD", "OK", or "TRUSTED".
	// BAD keys have serious problems and should not be used. If a key is OK, inspecting only
	// that key found no problems, but the system does not fully trust the key’s origin. A
	// TRUSTED key is valid, and the system knows enough about the key and its origin to trust
	// it. Not set for deleted keys.
	Status string
	// Problems is a list of human-readable problem strings found in the course of checking
	// whether the key is valid and trusted. Not set for deleted keys.
	Problems []string
}

// ParseTimestamp converts a timestamp from the Gerrit API to a time.Time in UTC.
func ParseTimestamp(timeStamp string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05.000000000", timeStamp, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// PatchSetNumber exists to allow parsing one stupid field in RevisionInfo which can end up
// being either a number or the string "edit".
type PatchSetNumber int

const PatchSetIsEdit PatchSetNumber = -1

func (p *PatchSetNumber) UnmarshalJSON(b []byte) error {
	jsonStr := string(b)
	if jsonStr == "null" {
		return nil
	}
	if jsonStr == "\"edit\"" {
		*p = PatchSetIsEdit
		return nil
	}
	num, err := strconv.Atoi(jsonStr)
	if err != nil {
		return err
	}
	*p = PatchSetNumber(num)
	return nil
}
