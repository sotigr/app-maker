package api

// JobPublishEvents is the job kind carrying a batch's events to the change
// log (spec 2.5, 9.2).
const JobPublishEvents = "publish_events"

// PublishEventsArgs are the arguments of a publish_events job.
type PublishEventsArgs struct {
	Events []Event `json:"events"`
}

// MaxPublishBytes is the size above which a batch's events are split into
// several publish_events jobs (spec 9.2).
const MaxPublishBytes = 1 << 20
