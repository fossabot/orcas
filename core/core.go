package core

import (
	"github.com/orca-zhang/idgen"
)

type ListOptions struct {
	Word  string // 过滤词，支持通配符*和?
	Delim string // 分隔符，每次请求后返回，原样回传即可
	Type  int    // 对象类型，0: 不过滤(default), 1: dir, 2: file, 3: version, 4: preview(thumb/m3u8/pdf)
	Count int    // 查询个数
	Order string // 排序方式，id/mtime/name/size/type 前缀 +: 升序（默认） -: 降序
	Brief int    // 显示更少内容(只在网络传输层，节省流量时有效)，0: FULL(default), 1: without EXT, 2:only ID
}

type Handler interface {
	// 传入underlying，返回当前的，构成链式调用
	New(h Handler) Handler
	Close()

	SetOptions(opt Options)
	PutBkt(c Ctx, o []*BucketInfo) error

	// 只有文件长度、HdrCRC32是预Ref，如果成功返回新DataID，失败返回0
	// 有文件长度、CRC32、MD5，成功返回引用的DataID，失败返回0，客户端发现DataID有变化，说明不需要上传数据
	// 如果非预Ref DataID传0，说明跳过了预Ref
	Ref(c Ctx, bktID int64, d []*DataInfo) ([]int64, error)
	// sn从0开始，DataID不传默认创建一个新的
	PutData(c Ctx, bktID, dataID int64, sn int, buf []byte) (int64, error)
	// 只传一个参数说明是sn，传两个参数说明是sn+offset，传三个参数说明是sn+offset+size
	GetData(c Ctx, bktID, id int64, sn int, offset ...int) ([]byte, error)
	// 上传元数据
	PutDataInfo(c Ctx, bktID int64, d []*DataInfo) ([]int64, error)
	// 获取数据信息
	GetDataInfo(c Ctx, bktID, id int64) (*DataInfo, error)
	// 用于非文件内容的扫描，只看文件是否存在，大小是否合适
	FileSize(c Ctx, bktID, dataID int64, sn int) (int64, error)

	// 垃圾回收时有数据没有元数据引用的为脏数据（需要留出窗口时间），有元数据没有数据的为损坏数据
	Put(c Ctx, bktID int64, o []*ObjectInfo) ([]int64, error)
	Get(c Ctx, bktID int64, ids []int64) ([]*ObjectInfo, error)
	List(c Ctx, bktID, pid int64, opt ListOptions) (o []*ObjectInfo, cnt int64, delim string, err error)

	Rename(c Ctx, bktID, id int64, name string) error
	MoveTo(c Ctx, bktID, id, pid int64) error

	Recycle(c Ctx, bktID, id int64) error
	Delete(c Ctx, bktID, id int64) error
}

type RWHandler struct {
	mo MetadataAdapter
	do DataAdapter
	ig *idgen.IDGen
}

func NewRWHandler() Handler {
	acm := &DefaultAccessCtrlMgr{}
	return &RWHandler{
		mo: NewDefaultMetadataAdapter(acm),
		do: NewDefaultDataAdapter(acm),
		ig: idgen.NewIDGen(nil, 0), // 需要改成配置
	}
}

// 传入underlying，返回当前的，构成链式调用
func (ch *RWHandler) New(Handler) Handler {
	// 忽略下层handler
	return ch
}

func (ch *RWHandler) Close() {
	ch.do.Close()
	ch.mo.Close()
}

func (ch *RWHandler) SetOptions(opt Options) {
	ch.do.SetOptions(opt)
}

func (ch *RWHandler) PutBkt(c Ctx, o []*BucketInfo) error {
	return ch.mo.PutBkt(c, o)
}

// 只有文件长度、HdrCRC32是预Ref，如果成功返回新DataID，失败返回0
// 有文件长度、CRC32、MD5，成功返回引用的DataID，失败返回0，客户端发现DataID有变化，说明不需要上传数据
// 如果非预Ref DataID传0，说明跳过了预Ref
func (ch *RWHandler) Ref(c Ctx, bktID int64, d []*DataInfo) ([]int64, error) {
	return ch.mo.RefData(c, bktID, d)
}

// 打包上传或者小文件，sn传-1，大文件sn从0开始，DataID不传默认创建一个新的
func (ch *RWHandler) PutData(c Ctx, bktID, dataID int64, sn int, buf []byte) (int64, error) {
	if dataID == 0 {
		if len(buf) <= 0 {
			dataID = EmptyDataID
		} else {
			dataID, _ = ch.ig.New()
		}
	}
	return dataID, ch.do.Write(c, bktID, dataID, sn, buf)
}

// 上传完数据以后，再创建元数据
func (ch *RWHandler) PutDataInfo(c Ctx, bktID int64, d []*DataInfo) (ids []int64, err error) {
	for _, x := range d {
		if x.ID == 0 {
			x.ID, _ = ch.ig.New()
		}
		ids = append(ids, x.ID)
	}
	return ids, ch.mo.PutData(c, bktID, d)
}

func (ch *RWHandler) GetDataInfo(c Ctx, bktID, id int64) (*DataInfo, error) {
	return ch.mo.GetData(c, bktID, id)
}

// 只传一个参数说明是sn，传两个参数说明是sn+offset，传三个参数说明是sn+offset+size
func (ch *RWHandler) GetData(c Ctx, bktID, id int64, sn int, offset ...int) ([]byte, error) {
	switch len(offset) {
	case 0:
		return ch.do.Read(c, bktID, id, sn)
	case 1:
		return ch.do.ReadBytes(c, bktID, id, sn, offset[0], -1)
	}
	return ch.do.ReadBytes(c, bktID, id, sn, offset[0], offset[1])
}

// 用于非文件内容的扫描，只看文件是否存在，大小是否合适
func (ch *RWHandler) FileSize(c Ctx, bktID, dataID int64, sn int) (int64, error) {
	return ch.do.FileSize(c, bktID, dataID, sn)
}

// 垃圾回收时有数据没有元数据引用的为脏数据（需要留出窗口时间），有元数据没有数据的为损坏数据
// PID支持用补码来直接引用当次还未上传的对象的ID
func (ch *RWHandler) Put(c Ctx, bktID int64, o []*ObjectInfo) ([]int64, error) {
	for _, x := range o {
		if x.ID == 0 {
			x.ID, _ = ch.ig.New()
		}
	}
	for _, x := range o {
		if x.PID < 0 && int(^x.PID) <= len(o) {
			x.PID = o[^x.PID].ID
		}
	}
	return ch.mo.PutObj(c, bktID, o)
}

func (ch *RWHandler) Get(c Ctx, bktID int64, ids []int64) ([]*ObjectInfo, error) {
	return ch.mo.GetObj(c, bktID, ids)
}

func (ch *RWHandler) List(c Ctx, bktID, pid int64, opt ListOptions) ([]*ObjectInfo, int64, string, error) {
	return ch.mo.ListObj(c, bktID, pid, opt.Word, opt.Delim, opt.Order, opt.Count, 0)
}

// 如果存在同名文件，会报错：Error: stepping, UNIQUE constraint failed: obj.name (19)
func (ch *RWHandler) Rename(c Ctx, bktID, id int64, name string) error {
	return ch.mo.SetObj(c, bktID, []string{"name"}, &ObjectInfo{ID: id, Name: name})
}

func (ch *RWHandler) MoveTo(c Ctx, bktID, id, pid int64) error {
	return ch.mo.SetObj(c, bktID, []string{"pid"}, &ObjectInfo{ID: id, PID: pid})
}

func (ch *RWHandler) Recycle(c Ctx, bktID, id int64) error {
	return ch.mo.SetObj(c, bktID, []string{"status"}, &ObjectInfo{ID: id, Status: OBJ_RECYCLED})
}

func (ch *RWHandler) Delete(c Ctx, bktID, id int64) error {
	return ch.mo.SetObj(c, bktID, []string{"status"}, &ObjectInfo{ID: id, Status: OBJ_DELETED})
}
