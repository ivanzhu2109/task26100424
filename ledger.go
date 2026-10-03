// Package archiveledger 实现归档版本保留账本。
// 卷策略与版本记录保存在进程内存中，不处理文件内容。
package archiveledger

import (
	"errors"
	"sort"
)

// secondsPerDay 一天固定的秒数。
const secondsPerDay int64 = 86400

// ErrUnknownVolume 表示登记版本时卷尚未提交过策略。
var ErrUnknownVolume = errors.New("archiveledger: unknown volume")

// version 表示一个已登记、尚未清除的版本。
type version struct {
	id            string
	volume        string
	registeredAt  int64
	committedDays int64
}

// deadline 返回版本当前有效截止时间：原始登记时间加承诺天数。
func (v *version) deadline() int64 {
	return v.registeredAt + v.committedDays*secondsPerDay
}

// volumePolicy 表示卷的当前策略。
type volumePolicy struct {
	retentionDays int64
	legalHold     bool
}

// Ledger 是归档版本保留账本。
type Ledger struct {
	volumes  map[string]*volumePolicy
	versions map[string]*version
}

// New 创建一个空账本。
func New() *Ledger {
	return &Ledger{
		volumes:  make(map[string]*volumePolicy),
		versions: make(map[string]*version),
	}
}

// SubmitPolicy 提交卷策略：设置当前保留天数与法律冻结开关。
// 上调保留天数时，该卷所有尚未清除版本的承诺天数变为原承诺与新天数中的较大值；
// 下调只影响之后登记的版本。法律冻结只阻止清除，不改变任何承诺。
func (l *Ledger) SubmitPolicy(now int64, volume string, retentionDays int64, legalHold bool) {
	p, ok := l.volumes[volume]
	if !ok {
		p = &volumePolicy{}
		l.volumes[volume] = p
	}
	if retentionDays > p.retentionDays {
		for _, v := range l.versions {
			if v.volume == volume && v.committedDays < retentionDays {
				v.committedDays = retentionDays
			}
		}
	}
	p.retentionDays = retentionDays
	p.legalHold = legalHold
}

// RegisterVersion 登记版本，承诺保留登记时卷策略规定的天数。
// 卷尚未提交过策略时返回 ErrUnknownVolume。
func (l *Ledger) RegisterVersion(now int64, id string, volume string) error {
	p, ok := l.volumes[volume]
	if !ok {
		return ErrUnknownVolume
	}
	l.versions[id] = &version{
		id:            id,
		volume:        volume,
		registeredAt:  now,
		committedDays: p.retentionDays,
	}
	return nil
}

// QueryVersion 查询版本当前有效截止时间及是否可以清除。
// 只有调用时间大于或等于有效截止时间且卷未冻结，才可清除。
// 版本不存在（未登记或已清除）时 ok 为 false。
func (l *Ledger) QueryVersion(now int64, id string) (deadline int64, purgeable bool, ok bool) {
	v, ok := l.versions[id]
	if !ok {
		return 0, false, false
	}
	deadline = v.deadline()
	purgeable = now >= deadline && !l.volumes[v.volume].legalHold
	return deadline, purgeable, true
}

// PurgeExpired 按卷清除到期版本，返回被清除的版本标识。
// 卷被法律冻结时不清除任何版本。清除按有效截止时间从早到晚选择，
// 同一截止时间按版本标识字典序选择。清除后版本不再参与查询和后续清除。
func (l *Ledger) PurgeExpired(now int64, volume string) []string {
	p, ok := l.volumes[volume]
	if !ok || p.legalHold {
		return nil
	}
	var expired []*version
	for _, v := range l.versions {
		if v.volume == volume && now >= v.deadline() {
			expired = append(expired, v)
		}
	}
	sort.Slice(expired, func(i, j int) bool {
		di, dj := expired[i].deadline(), expired[j].deadline()
		if di != dj {
			return di < dj
		}
		return expired[i].id < expired[j].id
	})
	purged := make([]string, 0, len(expired))
	for _, v := range expired {
		delete(l.versions, v.id)
		purged = append(purged, v.id)
	}
	return purged
}
