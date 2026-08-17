package tuiapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

var viewNames = []string{"Connections", "Inbox", "Message", "Agenda", "Operations / Cache"}

type snapshot struct {
	generation uint64
	scope string
	connections []model.Connection
	messages []model.Message
	events []model.Event
	mailCursor string
	mailNext string
	eventCursor string
	eventNext string
	cache string
	err string
}

type operationSnapshot struct {
	token string
	result model.OperationResult
	err error
}

type providerReadSnapshot struct {
	generation uint64
	kind string
	doctor model.DoctorResult
	detail model.MessageDetail
	attachment model.Attachment
	connection model.Connection
	data []byte
	err error
}

type posthouseApp struct {
	service *service.Service
	view *tui.State[int]
	selected *tui.State[int]
	loading *tui.State[bool]
	errorText *tui.State[string]
	status *tui.State[string]
	connections *tui.State[[]model.Connection]
	messages *tui.State[[]model.Message]
	events *tui.State[[]model.Event]
	detail *tui.State[model.MessageDetail]
	searching *tui.State[bool]
	query *tui.State[string]
	modal *tui.State[bool]
	modalText *tui.State[string]
	pendingToken *tui.State[string]
	pendingDiscover *tui.State[string]
	executingToken *tui.State[string]
	lastOperation *tui.State[model.OperationResult]
	lastOperationError *tui.State[string]
	editor *tui.State[bool]
	editorKind *tui.State[string]
	editorTick *tui.State[int]
	editorFields []*tui.State[string]
	app *tui.App
	mailCursor *tui.State[string]
	mailNext *tui.State[string]
	mailHistory *tui.State[[]string]
	eventCursor *tui.State[string]
	eventNext *tui.State[string]
	eventHistory *tui.State[[]string]
	attachment *tui.State[model.Attachment]
	attachmentData *tui.State[[]byte]
	updates chan snapshot
	operationUpdates chan operationSnapshot
	providerReadUpdates chan providerReadSnapshot
	executeOperation func(context.Context,string) (model.OperationResult,error)
	doctorConnection func(context.Context,string) (model.DoctorResult,error)
	discoverConnection func(context.Context,string) (model.Connection,error)
	getMessage func(context.Context,string,string,uint32) (model.MessageDetail,error)
	getAttachment func(context.Context,string,string,uint32,string) (model.Attachment,[]byte,error)
	ctx context.Context
	cancel context.CancelFunc
	refreshCancel context.CancelFunc
	operationCancel context.CancelFunc
	providerReadCancel context.CancelFunc
	refreshGeneration *atomic.Uint64
	providerReadGeneration *atomic.Uint64
	wg *sync.WaitGroup
}

func New(application *service.Service) *posthouseApp {
	ctx, cancel := context.WithCancel(context.Background())
	app := &posthouseApp{
		service: application,
		view: tui.NewState(0), selected: tui.NewState(0), loading: tui.NewState(false),
		errorText: tui.NewState(""), status: tui.NewState("Starting…"),
		connections: tui.NewState([]model.Connection{}), messages: tui.NewState([]model.Message{}),
		events: tui.NewState([]model.Event{}), detail: tui.NewState(model.MessageDetail{}),
		searching: tui.NewState(false), query: tui.NewState(""), modal: tui.NewState(false),
		modalText: tui.NewState(""), pendingToken: tui.NewState(""), pendingDiscover: tui.NewState(""),
		executingToken: tui.NewState(""),
		lastOperation: tui.NewState(model.OperationResult{}),
		lastOperationError: tui.NewState(""),
		editor: tui.NewState(false), editorKind: tui.NewState(""), editorTick: tui.NewState(0),
		mailCursor: tui.NewState(""), mailNext: tui.NewState(""), mailHistory: tui.NewState([]string{}),
		eventCursor: tui.NewState(""), eventNext: tui.NewState(""), eventHistory: tui.NewState([]string{}),
		attachment: tui.NewState(model.Attachment{}), attachmentData: tui.NewState([]byte{}),
		updates: make(chan snapshot, 4), operationUpdates: make(chan operationSnapshot, 1), providerReadUpdates: make(chan providerReadSnapshot, 1),
		executeOperation: application.ExecuteOperation, doctorConnection: application.DoctorConnection, discoverConnection: application.DiscoverConnection, getMessage: application.GetMessageContext, getAttachment: application.GetAttachment,
		ctx: ctx, cancel: cancel,
		refreshGeneration: &atomic.Uint64{}, providerReadGeneration: &atomic.Uint64{},
		wg: &sync.WaitGroup{},
	}
	app.refresh()
	return app
}

func Run(application *service.Service) error {
	component := New(application)
	app, err := tui.NewApp(tui.WithRootComponent(component), tui.WithoutMouse(), tui.WithFrameRate(30))
	if err != nil { component.close(); return err }
	defer app.Close()
	defer component.close()
	return app.Run()
}

func (p *posthouseApp) Watchers() []tui.Watcher {
	return []tui.Watcher{tui.Watch(p.updates, p.applySnapshot),tui.Watch(p.operationUpdates,p.applyOperation),tui.Watch(p.providerReadUpdates,p.applyProviderRead)}
}

func (p *posthouseApp) KeyMap() tui.KeyMap {
	if p.searching.Get() {
		return tui.KeyMap{
			tui.OnStop(tui.AnyRune, func(ke tui.KeyEvent) { p.query.Set(p.query.Get()+string(ke.Rune)) }),
			tui.OnStop(tui.KeyBackspace, func(ke tui.KeyEvent) { value := []rune(p.query.Get()); if len(value)>0 { p.query.Set(string(value[:len(value)-1])) } }),
			tui.OnStop(tui.KeyEnter, func(ke tui.KeyEvent) { p.searching.Set(false); p.resetMailPaging(); p.resetEventPaging(); p.refresh() }),
			tui.OnStop(tui.KeyEscape, func(ke tui.KeyEvent) { p.searching.Set(false); p.query.Set("") }),
		}
	}
	return tui.KeyMap{
		tui.On(tui.Rune('q'), func(ke tui.KeyEvent) { p.cancel(); ke.App().Stop() }),
		tui.On(tui.KeyTab, func(ke tui.KeyEvent) { p.switchView(1) }),
		tui.On(tui.KeyTab.Shift(), func(ke tui.KeyEvent) { p.switchView(-1) }),
		tui.On(tui.KeyDown, func(ke tui.KeyEvent) { p.move(1) }),
		tui.On(tui.KeyUp, func(ke tui.KeyEvent) { p.move(-1) }),
		tui.On(tui.Rune('j'), func(ke tui.KeyEvent) { p.move(1) }),
		tui.On(tui.Rune('k'), func(ke tui.KeyEvent) { p.move(-1) }),
		tui.On(tui.Rune('/'), func(ke tui.KeyEvent) { p.searching.Set(true) }),
		tui.On(tui.Rune('r'), func(ke tui.KeyEvent) { p.refresh() }),
		tui.On(tui.Rune('c'), p.createAction),
		tui.On(tui.Rune('a'), p.itemAction),
		tui.On(tui.Rune('d'), func(ke tui.KeyEvent) { p.discoverSelected() }),
		tui.On(tui.Rune('s'), func(ke tui.KeyEvent) { p.beginAttachmentSave() }),
		tui.On(tui.Rune('n'), func(ke tui.KeyEvent) { p.pageList(1) }),
		tui.On(tui.Rune('p'), func(ke tui.KeyEvent) { p.pageList(-1) }),
		tui.On(tui.KeyPageDown, func(ke tui.KeyEvent) { p.pageList(1) }),
		tui.On(tui.KeyPageUp, func(ke tui.KeyEvent) { p.pageList(-1) }),
		tui.On(tui.KeyEnter, p.openSelected),
		tui.On(tui.KeyEscape, func(ke tui.KeyEvent) { if p.view.Get()==2 { p.view.Set(1) } }),
		tui.On(tui.Rune('?'), func(ke tui.KeyEvent) { p.modalText.Set("Keyboard\n\nTab/Shift+Tab areas · j/k or arrows move · / search · r refresh\nc compose/create · a actions · d discover · s save attachment · n/p page\nEnter open/confirm · Esc back · q quit"); p.modal.Set(true) }),
	}
}

func (p *posthouseApp) refresh() { p.refreshScope("full") }

func (p *posthouseApp) refreshScope(scope string) {
	if p.refreshCancel != nil { p.refreshCancel() }
	ctx, cancel := context.WithCancel(p.ctx)
	p.refreshCancel = cancel
	p.loading.Set(true)
	p.status.Set("Refreshing live sources…")
	query := p.query.Get()
	mailCursor := p.mailCursor.Get()
	eventCursor := p.eventCursor.Get()
	connections := p.connections.Get()
	generation := p.refreshGeneration.Add(1)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		next := snapshot{generation:generation, scope:scope, mailCursor:mailCursor, eventCursor:eventCursor, connections:connections}
		if scope == "full" {
			loaded, err := p.service.Connections(model.Selector{})
			if err != nil { next.err = err.Error() } else { next.connections = loaded; connections = loaded }
		}
		if (scope == "full" || scope == "mail") && connectionsHaveCapability(connections,"mail.read") {
			messages, mailErr := p.service.SearchMessagesContext(ctx, model.Selector{}, mail.SearchOptions{Query: query}, mailPageSize, mailCursor)
			if mailErr == nil { next.messages = messages.Messages; next.mailNext = messages.NextCursor; next.err = appendSourceErrors(next.err,messages.Errors) } else { next.err = appendError(next.err, mailErr) }
		}
		if (scope == "full" || scope == "events") && connectionsHaveCapability(connections,"calendar.read") {
			events, calendarErr := p.service.ListEvents(ctx, model.Selector{}, time.Now().Add(-24*time.Hour), time.Now().Add(90*24*time.Hour), query, eventPageSize, eventCursor)
			if calendarErr == nil { next.events = events.Events; next.eventNext = events.NextCursor; next.err = appendSourceErrors(next.err,events.Errors) } else { next.err = appendError(next.err, calendarErr) }
		}
		if scope == "full" {
			if cache, cacheErr := p.service.CacheStatus(ctx); cacheErr == nil { next.cache = fmt.Sprintf("%d entries · %.1f MiB / %.1f MiB", cache.Entries, float64(cache.Bytes)/(1<<20), float64(cache.MaxBytes)/(1<<20)) } else { next.cache = "cache unavailable: "+cacheErr.Error() }
		}
		if ctx.Err()!=nil { return }
		select { case p.updates <- next: case <-ctx.Done(): }
	}()
}

func (p *posthouseApp) close() { p.cancel(); if p.refreshCancel!=nil { p.refreshCancel() }; if p.operationCancel!=nil { p.operationCancel() }; if p.providerReadCancel!=nil { p.providerReadCancel() }; p.wg.Wait() }

func (p *posthouseApp) applySnapshot(next snapshot) {
	if next.generation != p.refreshGeneration.Load() { return }
	p.loading.Set(false)
	if next.scope == "full" {
		p.connections.Set(next.connections)
		p.status.Set(next.cache)
	}
	if next.scope == "full" || next.scope == "mail" {
		p.messages.Set(next.messages)
		p.mailCursor.Set(next.mailCursor)
		p.mailNext.Set(next.mailNext)
	}
	if next.scope == "full" || next.scope == "events" {
		p.events.Set(next.events)
		p.eventCursor.Set(next.eventCursor)
		p.eventNext.Set(next.eventNext)
	}
	if next.err != "" || next.scope == "full" {
		p.errorText.Set(next.err)
	}
	p.clampSelection()
}

func (p *posthouseApp) switchView(delta int) {
	view := (p.view.Get()+delta+len(viewNames))%len(viewNames)
	p.view.Set(view); p.selected.Set(0)
}

func (p *posthouseApp) itemCount() int {
	switch p.view.Get() { case 0: return len(p.connections.Get()); case 1: return len(p.messages.Get()); case 2: return max(1,len(p.detail.Get().Attachments)); case 3: return len(p.events.Get()); default: return 1 }
}

func (p *posthouseApp) move(delta int) {
	count := p.itemCount(); if count == 0 { return }
	next := p.selected.Get()+delta; if next<0 { next=0 }; if next>=count { next=count-1 }; p.selected.Set(next)
}

func (p *posthouseApp) clampSelection() { p.move(0) }

func (p *posthouseApp) openSelected(ke tui.KeyEvent) {
	switch p.view.Get() {
	case 0:
		items := p.connections.Get(); if len(items)==0 { return }
		id:=items[p.selected.Get()].ID
		p.startProviderRead("doctor",func(ctx context.Context) providerReadSnapshot { result,err:=p.doctorConnection(ctx,id); return providerReadSnapshot{doctor:result,err:err} })
	case 1:
		items := p.messages.Get(); if len(items)==0 { return }
		item := items[p.selected.Get()]
		p.startProviderRead("message",func(ctx context.Context) providerReadSnapshot { detail,err:=p.getMessage(ctx,item.ConnectionID,item.Folder,item.UID); return providerReadSnapshot{detail:detail,err:err} })
	case 2:
		detail:=p.detail.Get(); attachments:=detail.Attachments; if len(attachments)==0{return}
		attachment:=attachments[p.selected.Get()]
		p.startProviderRead("attachment",func(ctx context.Context) providerReadSnapshot { metadata,data,err:=p.getAttachment(ctx,detail.ConnectionID,detail.Folder,detail.UID,attachment.ID); return providerReadSnapshot{attachment:metadata,data:data,err:err} })
	}
}

func (p *posthouseApp) startProviderRead(kind string, read func(context.Context) providerReadSnapshot) {
	if p.providerReadCancel!=nil { p.providerReadCancel() }
	ctx,cancel:=context.WithCancel(p.ctx); p.providerReadCancel=cancel; generation:=p.providerReadGeneration.Add(1)
	p.loading.Set(true); p.errorText.Set(""); p.modalText.Set("Loading provider data…\n\nEsc cancels this request."); p.modal.Set(true)
	p.wg.Add(1)
	go func() { defer p.wg.Done(); next:=read(ctx); next.generation=generation; next.kind=kind; select { case p.providerReadUpdates<-next: case <-ctx.Done(): } }()
}

func (p *posthouseApp) applyProviderRead(next providerReadSnapshot) {
	if next.generation!=p.providerReadGeneration.Load(){return}
	p.providerReadCancel=nil; p.loading.Set(false)
	if next.err!=nil { p.errorText.Set(next.err.Error()); p.modalText.Set("Provider read failed\n\n"+next.err.Error()); p.modal.Set(true); return }
	switch next.kind {
	case "doctor": p.modalText.Set(formatDoctor(next.doctor)); p.modal.Set(true)
	case "discover": p.modalText.Set(formatDiscover(next.connection)); p.modal.Set(true); p.refresh()
	case "message": p.detail.Set(next.detail); p.view.Set(2); p.selected.Set(0); p.modal.Set(false)
	case "attachment":
		p.attachment.Set(next.attachment); p.attachmentData.Set(next.data)
		preview:=""; if strings.HasPrefix(strings.ToLower(next.attachment.ContentType),"text/") { preview=string(next.data[:min(len(next.data),16<<10)]) }
		p.modalText.Set(fmt.Sprintf("Attachment loaded\n\n%s\n%s · %d bytes\n\ns saves to disk · Esc closes\n\n%s",next.attachment.Name,next.attachment.ContentType,len(next.data),preview)); p.modal.Set(true)
	case "attachment-save":
		p.attachment.Set(next.attachment); p.attachmentData.Set(next.data)
		name:=next.attachment.Name; if name=="" { name="attachment" }
		p.beginEditor("save", []string{name})
	}
}

func (p *posthouseApp) itemAction(ke tui.KeyEvent) {
	if p.view.Get()==3 && len(p.events.Get())>0 { event:=p.events.Get()[p.selected.Get()]; p.beginEditor("event-action",[]string{"update",event.Title,event.Start.Format(time.RFC3339),event.End.Format(time.RFC3339)}); return }
	if _,ok:=p.selectedMessage(); !ok { p.modalText.Set("No write action is available for this selection."); p.modal.Set(true); return }
	p.beginEditor("action",[]string{"mark-read","","text",""})
}

func (p *posthouseApp) createAction(ke tui.KeyEvent) {
	switch p.view.Get() {
	case 0:
		p.beginEditor("connection", []string{"","","","","","","","","","",""})
	case 1, 2:
		connection := p.defaultConnection("mail.send")
		p.beginEditor("mail", []string{connection,"send","","","","","text","",""})
	case 3:
		connection, collection := p.defaultCalendarTarget()
		now := time.Now().Add(time.Hour).Truncate(15*time.Minute)
		p.beginEditor("event", []string{connection,collection,"",now.Format(time.RFC3339),now.Add(time.Hour).Format(time.RFC3339)})
	default:
		p.modalText.Set("Create is available from Inbox (mail) or Agenda (CalDAV event).")
		p.modal.Set(true)
	}
}

func (p *posthouseApp) defaultConnection(capability string) string { for _,connection:=range p.connections.Get() { if slices.Contains(connection.Capabilities,capability) { return connection.ID } }; return "" }
func splitValues(value string) []string { var result []string; for _,item:=range strings.Split(value,",") { if item=strings.TrimSpace(item); item!="" { result=append(result,item) } }; return result }
func connectionsHaveCapability(connections []model.Connection, capability string) bool { for _,connection:=range connections { if slices.Contains(connection.Capabilities,capability) { return true } }; return false }
func (p *posthouseApp) defaultCalendarTarget() (string,string) { for _,connection:=range p.connections.Get() { if connection.Calendar==nil || !slices.Contains(connection.Capabilities,"calendar.write") { continue }; for _,collection:=range connection.Calendar.Collections { if !collection.ReadOnly { return connection.ID,collection.ID } }; return connection.ID,"" }; return "","" }
func (p *posthouseApp) selectedMessage() (model.Message,bool) { if p.view.Get()==2 && p.detail.Get().UID!=0 { return p.detail.Get().Message,true }; if p.view.Get()==1 { items:=p.messages.Get(); if len(items)>0 && p.selected.Get()<len(items) { return items[p.selected.Get()],true } }; return model.Message{},false }

func (p *posthouseApp) confirmModal(ke tui.KeyEvent) {
	if p.editor.Get() || p.executingToken.Get()!="" || p.providerReadCancel != nil { return }
	if id := p.pendingDiscover.Get(); id != "" { p.startDiscover(id); return }
	token := p.pendingToken.Get()
	if token=="" { p.modal.Set(false); return }
	ctx,cancel:=context.WithCancel(p.ctx); p.operationCancel=cancel; p.pendingToken.Set(""); p.executingToken.Set(token); p.modalText.Set("Executing prepared operation…\n\nEsc cancels this request; provider ambiguity remains recorded safely.")
	p.wg.Add(1)
	go func() { defer p.wg.Done(); result,err:=p.executeOperation(ctx,token); next:=operationSnapshot{token:token,result:result,err:err}; select { case p.operationUpdates<-next: case <-p.ctx.Done(): } }()
}

func (p *posthouseApp) cancelModal() {
	if p.operationCancel != nil { p.operationCancel(); p.operationCancel = nil }
	if p.providerReadCancel != nil { p.providerReadCancel(); p.providerReadCancel = nil; p.providerReadGeneration.Add(1); p.loading.Set(false) }
	p.editor.Set(false)
	p.editorFields = nil
	p.editorKind.Set("")
	p.modal.Set(false)
	p.pendingToken.Set("")
	p.pendingDiscover.Set("")
}
func (p *posthouseApp) applyOperation(next operationSnapshot) { if next.token!=p.executingToken.Get(){return}; p.operationCancel=nil; p.executingToken.Set(""); if next.result.Token=="" {next.result.Token=next.token}; p.lastOperation.Set(next.result); operationError:=""; if next.err!=nil {operationError=next.err.Error()}; p.lastOperationError.Set(operationError); summary:=formatOperationResult(next.result,operationError); canReplaceModal:=p.modal.Get()&&p.pendingToken.Get()==""; if next.err!=nil { p.errorText.Set(summary); if canReplaceModal {p.modalText.Set(summary)} } else if canReplaceModal { p.modalText.Set(summary) }; p.refresh() }

func appendError(current string, err error) string { if current=="" { return err.Error() }; return current+" · "+err.Error() }
func appendSourceErrors(current string, sourceErrors []model.SourceError) string { for _,sourceError:=range sourceErrors { source:=sourceError.ConnectionID; if sourceError.CollectionID!="" {source+="/"+sourceError.CollectionID}; message:=sourceError.Message; if message=="" {message=sourceError.Code}; if current=="" {current=source+": "+message} else {current+=" · "+source+": "+message} }; return current }
func formatOperationResult(result model.OperationResult, operationError string) string { summary:=fmt.Sprintf("Operation %s\n\nStatus: %s\nResult: %v",result.Token,result.Status,result.Result); if operationError!="" {summary+="\n\nError: "+operationError}; return summary }
func formatDoctor(result model.DoctorResult) string { lines:=[]string{"Connection doctor: "+result.ConnectionID}; for _,check:=range result.Checks { lines=append(lines, fmt.Sprintf("%s  %-20s %s", strings.ToUpper(check.Status),check.Name,check.Message)) }; return strings.Join(lines,"\n") }
func formatPreview(operation model.PreparedOperation) string { return fmt.Sprintf("Confirm prepared operation\n\nConnection: %s\nIdentity: %s <%s>\nKind: %s\nExpires: %s\n\n%v\n\nEnter executes · Esc cancels",operation.ConnectionID,operation.Identity.Name,operation.Identity.Email,operation.Kind,operation.ExpiresAt.Local().Format(time.Kitchen),operation.Preview) }
func selectedClass(selected, index int) string { if selected==index { return "font-bold inverse" }; return "" }
func unreadMarker(message model.Message) string { if message.Unread { return "●" }; return " " }

templ (p *posthouseApp) Render() {
	<div class="flex-col h-full p-1 gap-1">
		<div class="flex shrink-0 border-rounded px-1 justify-between">
			<span class="font-bold">POSTHOUSE</span>
			<span>{viewNames[p.view.Get()]}</span>
			if p.loading.Get() { <span class="text-yellow">loading</span> } else { <span class="text-green">ready</span> }
		</div>
		<div class="flex gap-1 flex-grow overflow-hidden">
			<div class="flex-col border-rounded p-1 w-22 shrink-0">
				for index, name := range viewNames { <span class={selectedClass(p.view.Get(),index)}>{fmt.Sprintf("%d  %s",index+1,name)}</span> }
			</div>
			<div class="flex-col border-rounded p-1 flex-grow overflow-hidden">
				if p.view.Get()==0 {
					<span class="font-bold">Connection onboarding / doctor</span><hr />
					for index, connection := range p.connections.Get() { <span class={selectedClass(p.selected.Get(),index)}>{fmt.Sprintf("%-16s %-10s %s",connection.ID,connection.Category,strings.Join(connection.Capabilities,","))}</span> }
					if len(p.connections.Get())==0 { <span class="font-dim">No connections. Press c to onboard one or import config v2 JSON.</span> }
				} else if p.view.Get()==1 {
					<span class="font-bold">{fmt.Sprintf("Unified inbox · %s", moreMarker(p.mailNext.Get(), len(p.messages.Get())))}</span><hr />
					for index, message := range p.messages.Get() { <span class={selectedClass(p.selected.Get(),index)}>{fmt.Sprintf("%s %-12s %-22s %s",unreadMarker(message),message.ConnectionID,message.Subject,message.Preview)}</span> }
					if len(p.messages.Get())==0 { <span class="font-dim">No messages in this view.</span> }
				} else if p.view.Get()==2 {
					<span class="font-bold">Message detail / attachments</span><hr />
					<span>{p.detail.Get().Subject}</span><span class="font-dim">{fmt.Sprintf("From %v · %d attachments",p.detail.Get().From,len(p.detail.Get().Attachments))}</span><br /><span>{p.detail.Get().Text}</span><br />
					for index, attachment := range p.detail.Get().Attachments { <span class={selectedClass(p.selected.Get(),index)}>{fmt.Sprintf("attachment  %-28s %s · %d bytes",attachment.Name,attachment.ContentType,attachment.Size)}</span> }
				} else if p.view.Get()==3 {
					<span class="font-bold">{fmt.Sprintf("Unified agenda · %s", moreMarker(p.eventNext.Get(), len(p.events.Get())))}</span><hr />
					for index, event := range p.events.Get() { <span class={selectedClass(p.selected.Get(),index)}>{fmt.Sprintf("%s  %-12s %s",event.Start.Local().Format("Jan 02 15:04"),event.ConnectionID,event.Title)}</span> }
					if len(p.events.Get())==0 { <span class="font-dim">No events in the next 90 days.</span> }
				} else {
					<span class="font-bold">Operations / encrypted cache</span><hr /><span>{p.status.Get()}</span><br />
					if p.lastOperation.Get().Token!="" { <span>{formatOperationResult(p.lastOperation.Get(),p.lastOperationError.Get())}</span><br /> }
					<span class="font-dim">All writes are prepared, previewed, and idempotently executed.</span>
				}
			</div>
		</div>
		if p.errorText.Get()!="" { <div class="border-rounded border-red px-1 shrink-0"><span class="text-red">{p.errorText.Get()}</span></div> }
		<div class="flex shrink-0 px-1 justify-between"><span class="font-dim">Tab areas · j/k move · / search · r refresh · c create · a actions · d discover · n/p page</span><span class="font-dim">built by Tim Borovkov · s save · ? help · q quit</span></div>
		if p.searching.Get() { <div class="border-rounded px-1 shrink-0"><span class="text-cyan font-bold">Search: </span><span>{p.query.Get()+"_"}</span></div> }
		<modal open={p.modal} class="justify-center items-center" backdrop="dim" closeOnEscape={false} keyMap={p.modalKeyMap()}>
			<div class="w-70 border-rounded p-2 flex-col gap-1">
				if p.editor.Get() {
					<span class="font-bold">{p.editorTitle()}</span>
					<span class="font-dim">{p.editorHelp()}</span>
					<hr />
					for index, label := range p.editorLabels() {
						<div class="flex gap-1 items-center">
							<span class="w-20 shrink-0">{label}</span>
							if index < len(p.editorFields) && p.editorFieldIsBody(index) {
								<textarea key={p.editorFieldKey(index)} value={p.editorFields[index]} width={42} maxHeight={8} border={tui.BorderRounded} submitKey={tui.KeyF2} onSubmit={p.submitEditorText} />
							} else if index < len(p.editorFields) {
								<input key={p.editorFieldKey(index)} value={p.editorFields[index]} width={42} border={tui.BorderRounded} placeholder={p.editorPlaceholder(index)} onSubmit={p.submitEditorText} />
							}
							if p.rfc3339Mark(index)=="✓" { <span class="text-green">✓</span> }
							if p.rfc3339Mark(index)=="×" { <span class="text-red">×</span> }
							if p.rfc3339Error(index)!="" { <span class="text-red">{p.rfc3339Error(index)}</span> }
						</div>
					}
					if p.errorText.Get()!="" { <span class="text-red">{p.errorText.Get()}</span> }
				} else {
					<span>{p.modalText.Get()}</span>
					if p.executingToken.Get()!="" || p.providerReadCancel != nil {
						<span class="font-dim">Esc cancels this request</span>
					} else {
						<button class="focusable border-rounded px-1" onActivate={p.confirmModalAction}>Enter confirms · Esc cancels</button>
					}
				}
			</div>
		</modal>
	</div>
}
