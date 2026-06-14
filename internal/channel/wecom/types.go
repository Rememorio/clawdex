package wecom

import (
	"encoding/json"
	"encoding/xml"
	"strings"
)

// ── Inbound XML types ──

// xmlEnvelope is the outer encrypted wrapper received from WeCom (notification bot).
type xmlEnvelope struct {
	XMLName xml.Name `xml:"xml"`
	Encrypt string   `xml:"Encrypt"`
}

// jsonEncryptEnvelope is the outer encrypted wrapper received from WeCom (AI bot).
type jsonEncryptEnvelope struct {
	Encrypt string `json:"encrypt"`
}

// xmlFrom represents the sender in a WeCom message.
type xmlFrom struct {
	UserID string `xml:"UserId"`
	Name   string `xml:"Name"`
	Alias  string `xml:"Alias"`
}

// xmlArticle represents a single item in a news (图文) message.
type xmlArticle struct {
	Title  string `xml:"Title"`
	Desc   string `xml:"Description"`
	PicURL string `xml:"PicUrl"`
	URL    string `xml:"Url"`
}

// xmlMsgItem represents a single item in a mixed (图文混排) message.
type xmlMsgItem struct {
	MsgType string `xml:"MsgType"`
	Content string `xml:"Text>Content"`   // text item
	PicURL  string `xml:"Image>ImageUrl"` // image item
}

// xmlMessage is the decrypted inbound message from WeCom.
// Some fields are nested (e.g. <Text><Content>), others are top-level.
type xmlMessage struct {
	XMLName    xml.Name     `xml:"xml"`
	WebhookURL string       `xml:"WebhookUrl"`
	ChatID     string       `xml:"ChatId"`
	ChatType   string       `xml:"ChatType"` // "single" or "group"
	From       xmlFrom      `xml:"From"`
	MsgType    string       `xml:"MsgType"`
	Content    string       `xml:"Text>Content"`   // text: nested <Text><Content>
	PicURL     string       `xml:"Image>ImageUrl"` // image: nested <Image><ImageUrl>
	MediaID    string       `xml:"MediaId"`        // voice/file media ID (top-level)
	MsgID      string       `xml:"MsgId"`
	Event      string       `xml:"Event"`
	FileName   string       `xml:"FileName"` // file message
	FileSize   string       `xml:"FileSize"` // file message
	Title      string       `xml:"Title"`    // link message
	Desc       string       `xml:"Description"`
	LinkURL    string       `xml:"Url"`                  // link message
	Articles   []xmlArticle `xml:"Articles>Item"`        // news (图文) message
	MixedItems []xmlMsgItem `xml:"MixedMessage>MsgItem"` // mixed (图文混排) message
}

// ── Outbound JSON types ──

// markdownPayload is a WeCom webhook markdown message.
type markdownPayload struct {
	MsgType  string          `json:"msgtype"`
	Markdown markdownContent `json:"markdown"`
	ChatID   string          `json:"chatid,omitempty"`
}

type markdownContent struct {
	Content string `json:"content"`
}

// imagePayload is a WeCom webhook image message (base64 + md5).
type imagePayload struct {
	MsgType string       `json:"msgtype"`
	Image   imageContent `json:"image"`
	ChatID  string       `json:"chatid,omitempty"`
}

type imageContent struct {
	Base64 string `json:"base64"`
	MD5    string `json:"md5"`
}

// filePayload is a WeCom webhook file message.
type filePayload struct {
	MsgType string      `json:"msgtype"`
	File    fileContent `json:"file"`
	ChatID  string      `json:"chatid,omitempty"`
}

type fileContent struct {
	MediaID string `json:"media_id"`
}

// apiResponse is the standard WeCom API response.
type apiResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// mediaUploadResponse extends apiResponse with uploaded media info.
type mediaUploadResponse struct {
	apiResponse
	Type      string `json:"type"`
	MediaID   string `json:"media_id"`
	CreatedAt string `json:"created_at"`
}

// ── WebSocket frame types ──

// wsFrameHeaders carries per-frame metadata.
type wsFrameHeaders struct {
	ReqID string `json:"req_id,omitempty"`
}

// wsOutboundFrame is a frame sent to the WeCom WebSocket server.
type wsOutboundFrame struct {
	Command string         `json:"cmd"`
	Headers wsFrameHeaders `json:"headers,omitempty"`
	Body    any            `json:"body,omitempty"`
}

// wsInboundFrame is a frame received from the WeCom WebSocket server.
// WeCom may use either "cmd" or "command" as the field name, so we
// use a custom UnmarshalJSON to handle both.
type wsInboundFrame struct {
	Command string
	Headers wsFrameHeaders
	Body    json.RawMessage
	ErrCode int
	ErrMsg  string
}

func (f *wsInboundFrame) UnmarshalJSON(data []byte) error {
	var raw struct {
		Cmd           string          `json:"cmd,omitempty"`
		LegacyCommand string          `json:"command,omitempty"`
		Headers       wsFrameHeaders  `json:"headers,omitempty"`
		Body          json.RawMessage `json:"body,omitempty"`
		ErrCode       int             `json:"errcode,omitempty"`
		ErrMsg        string          `json:"errmsg,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.Command = raw.Cmd
	if f.Command == "" {
		f.Command = raw.LegacyCommand
	}
	f.Headers = raw.Headers
	f.Body = raw.Body
	f.ErrCode = raw.ErrCode
	f.ErrMsg = raw.ErrMsg
	return nil
}

// wsSubscribeBody is the body of an aibot_subscribe command.
type wsSubscribeBody struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`
}

// wsReplyBody is the body of an aibot_respond_msg command.
type wsReplyBody struct {
	MsgType      string           `json:"msgtype"`
	Markdown     *wsMarkdown      `json:"markdown,omitempty"`
	Stream       *wsStreamPayload `json:"stream,omitempty"`
	TemplateCard *templateCard    `json:"template_card,omitempty"`
	File         *wsMediaRef      `json:"file,omitempty"`
	Image        *wsMediaRef      `json:"image,omitempty"`
	Voice        *wsMediaRef      `json:"voice,omitempty"`
	Video        *wsVideoRef      `json:"video,omitempty"`
}

// wsSendBody is the body of an aibot_send_msg proactive message.
type wsSendBody struct {
	ChatID   string      `json:"chatid,omitempty"`
	MsgType  string      `json:"msgtype"`
	Markdown *wsMarkdown `json:"markdown,omitempty"`
}

// templateCard is a WeCom interactive card.
// CardType "text_notice" provides simple jump buttons (JumpList, max 3).
// CardType "button_interaction" provides rich buttons (ButtonList, max 6)
// and an optional dropdown selector (ButtonSelection, max 10 options).
type templateCard struct {
	CardType        string                   `json:"card_type"`
	MainTitle       *templateCardMainTitle   `json:"main_title,omitempty"`
	SubTitleText    string                   `json:"sub_title_text,omitempty"`
	JumpList        []templateCardJumpAction `json:"jump_list,omitempty"`
	ButtonSelection *templateCardSelection   `json:"button_selection,omitempty"`
	ButtonList      []templateCardButton     `json:"button_list,omitempty"`
	CardAction      *templateCardCardAction  `json:"card_action,omitempty"`
	TaskID          string                   `json:"task_id,omitempty"`
}

const (
	templateCardTypeTextNotice        = "text_notice"
	templateCardTypeButtonInteraction = "button_interaction"

	templateCardButtonStyleDefault = 1

	// templateCardButtonLimit is the max buttons per button_interaction card.
	templateCardButtonLimit = 6
	// templateCardSelectionLimit is the max options in a ButtonSelection dropdown.
	templateCardSelectionLimit = 10
)

type templateCardMainTitle struct {
	Title string `json:"title,omitempty"`
	Desc  string `json:"desc,omitempty"`
}

type templateCardJumpAction struct {
	Type     int    `json:"type"`
	Title    string `json:"title"`
	Question string `json:"question,omitempty"`
}

type templateCardCardAction struct {
	Type int    `json:"type"`
	URL  string `json:"url"`
}

// templateCardSelection is a dropdown selector inside a button_interaction card.
type templateCardSelection struct {
	QuestionKey string               `json:"question_key,omitempty"`
	Title       string               `json:"title,omitempty"`
	SelectedID  string               `json:"selected_id,omitempty"`
	OptionList  []templateCardOption `json:"option_list,omitempty"`
}

// templateCardOption is one option in a templateCardSelection dropdown.
type templateCardOption struct {
	ID        string `json:"id,omitempty"`
	Text      string `json:"text,omitempty"`
	IsChecked bool   `json:"is_checked,omitempty"`
}

// templateCardButton is a clickable button in a button_interaction card.
type templateCardButton struct {
	Text  string `json:"text,omitempty"`
	Style int    `json:"style,omitempty"`
	Key   string `json:"key,omitempty"`
}

// TemplateCardEvent is the callback payload when a user clicks a button_interaction card.
type TemplateCardEvent struct {
	EventKey      string                          `json:"event_key,omitempty"`
	TaskID        string                          `json:"task_id,omitempty"`
	SelectedItems templateCardSelectedItemWrapper `json:"selected_items,omitempty"`
}

type templateCardSelectedItemWrapper struct {
	SelectedItem []templateCardSelectedItem `json:"selected_item,omitempty"`
}

type templateCardSelectedItem struct {
	QuestionKey string                    `json:"question_key,omitempty"`
	OptionIDs   templateCardOptionIDArray `json:"option_ids,omitempty"`
}

type templateCardOptionIDArray struct {
	OptionID []string `json:"option_id,omitempty"`
}

// selectedTemplateCardOption extracts the selected option ID for a given question key.
func selectedTemplateCardOption(event *TemplateCardEvent, questionKey string) string {
	if event == nil {
		return ""
	}
	for _, item := range event.SelectedItems.SelectedItem {
		if strings.TrimSpace(item.QuestionKey) != strings.TrimSpace(questionKey) {
			continue
		}
		for _, optionID := range item.OptionIDs.OptionID {
			if id := strings.TrimSpace(optionID); id != "" {
				return id
			}
		}
	}
	return ""
}

type wsMarkdown struct {
	Content string `json:"content"`
}

type wsStreamPayload struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content,omitempty"`
}

// wsTemplateCardUpdateBody is the body for aibot_respond_update_msg.
// Used to update a button_interaction card in-place when a user clicks a button.
type wsTemplateCardUpdateBody struct {
	ResponseType string        `json:"response_type,omitempty"`
	TemplateCard *templateCard `json:"template_card,omitempty"`
}

// ── WebSocket inbound message types (JSON, not XML) ──

// wsMessage is the JSON-formatted message received via WebSocket callback.
type wsMessage struct {
	MsgID       string         `json:"msgid,omitempty"`
	ChatID      string         `json:"chatid,omitempty"`
	ChatType    string         `json:"chattype,omitempty"`
	MsgType     string         `json:"msgtype,omitempty"`
	From        wsFrom         `json:"from,omitempty"`
	Text        wsTextContent  `json:"text,omitempty"`
	Image       wsImageContent `json:"image,omitempty"`
	Voice       wsVoiceContent `json:"voice,omitempty"`
	File        wsFileContent  `json:"file,omitempty"`
	Video       wsFileContent  `json:"video,omitempty"`
	Mixed       wsMixedContent `json:"mixed,omitempty"`
	Event       wsEventContent `json:"event,omitempty"`
	Quote       *wsQuote       `json:"quote,omitempty"` // referenced/quoted message (AI Bot)
	ResponseURL string         `json:"response_url,omitempty"`
	AIBotID     string         `json:"aibotid,omitempty"`
}

type wsFrom struct {
	UserID string `json:"userid,omitempty"`
	Name   string `json:"name,omitempty"`
	Alias  string `json:"alias,omitempty"`
}

type wsTextContent struct {
	Content string `json:"content,omitempty"`
}

type wsImageContent struct {
	URL    string `json:"url,omitempty"`
	AESKey string `json:"aeskey,omitempty"` // per-image decrypt key
}

type wsVoiceContent struct {
	MediaID string `json:"media_id,omitempty"`
	Content string `json:"content,omitempty"` // transcribed text (AI bot)
}

type wsFileContent struct {
	URL    string `json:"url,omitempty"`
	AESKey string `json:"aeskey,omitempty"` // per-file decrypt key
}

type wsMixedContent struct {
	MsgItem []wsMixedItem `json:"msg_item,omitempty"`
}

type wsMixedItem struct {
	MsgType string         `json:"msgtype,omitempty"`
	Text    wsTextContent  `json:"text,omitempty"`
	Image   wsImageContent `json:"image,omitempty"`
	File    wsFileContent  `json:"file,omitempty"`
}

type wsEventContent struct {
	EventType         string             `json:"eventtype,omitempty"`
	TemplateCardEvent *TemplateCardEvent `json:"template_card_event,omitempty"`
}

// wsQuote contains a referenced/quoted message (AI Bot only).
// When a user quotes a previous message (text, image, file, etc.) and sends a
// new message, the quoted content appears in this field.
type wsQuote struct {
	MsgType string         `json:"msgtype,omitempty"`
	Text    wsTextContent  `json:"text,omitempty"`
	Image   wsImageContent `json:"image,omitempty"`
	Voice   wsVoiceContent `json:"voice,omitempty"`
	File    wsFileContent  `json:"file,omitempty"`
	Video   wsFileContent  `json:"video,omitempty"`
	Mixed   wsMixedContent `json:"mixed,omitempty"`
}
