package splitkit

import "encoding/json"

// Block is the current Split Kit value block carried by a remoteValue event.
type Block struct {
	Title     string `json:"title,omitempty"`
	FeedGUID  string `json:"feedGuid,omitempty"`
	ItemGUID  string `json:"itemGuid,omitempty"`
	EventGUID string `json:"eventGuid,omitempty"`
	BlockGUID string `json:"blockGuid,omitempty"`
	EventAPI  string `json:"eventAPI,omitempty"`
	Image     string `json:"image,omitempty"`
	Value     *Value `json:"value,omitempty"`
}

// Value holds the block's split destinations.
type Value struct {
	Destinations []Destination `json:"destinations,omitempty"`
}

// Destination is one recipient in a value block.
type Destination struct {
	Name        string     `json:"name,omitempty"`
	Address     string     `json:"address,omitempty"`
	Type        string     `json:"type,omitempty"`
	Split       FlexString `json:"split,omitempty"`
	Fee         bool       `json:"fee,omitempty"`
	CustomKey   FlexString `json:"customKey,omitempty"`
	CustomValue string     `json:"customValue,omitempty"`
}

// FlexString unmarshals a JSON string or number into text.
type FlexString string

// UnmarshalJSON accepts a JSON string or number.
func (f *FlexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	*f = FlexString(b)
	return nil
}
