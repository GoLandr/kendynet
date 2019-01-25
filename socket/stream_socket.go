/*
*  tcp或unix域套接字会话
 */

package socket

import (
	"bufio"
	"github.com/sniperHW/kendynet"
	"github.com/sniperHW/kendynet/util"
	"io"
	"net"
	//"sync"
	"time"
)

type StreamSocket struct {
	*SocketBase
	conn net.Conn
}

func (this *StreamSocket) Close(reason string, delay time.Duration) {
	this.mutex.Lock()
	if (this.flag & closed) > 0 {
		this.mutex.Unlock()
		return
	}

	this.closeReason = reason
	this.flag |= (closed | rclosed)
	if this.flag&wclosed > 0 {
		delay = 0 //写端已经关闭，delay参数没有意义设置为0
	}

	this.sendQue.Close()
	this.mutex.Unlock()
	if this.sendQue.Len() > 0 {
		delay = delay * time.Second
		if delay <= 0 {
			this.sendQue.Clear()
		}
	}
	if delay > 0 {
		this.shutdownRead()
		ticker := time.NewTicker(delay)
		go func() {
			/*
			 *	delay > 0,sendThread最多需要经过delay秒之后才会结束，
			 *	为了避免阻塞调用Close的goroutine,启动一个新的goroutine在chan上等待事件
			 */
			select {
			case <-this.sendCloseChan:
			case <-ticker.C:
			}
			ticker.Stop()
			this.doClose()
		}()
	} else {
		this.doClose()
	}

}

func (this *StreamSocket) sendMessage(msg kendynet.Message) error {
	if msg == nil {
		return kendynet.ErrInvaildBuff
	} else if (this.flag&closed) > 0 || (this.flag&wclosed) > 0 {
		return kendynet.ErrSocketClose
	} else {
		fullReturn := true
		err := this.sendQue.AddNoWait(msg, fullReturn)
		if nil != err {
			if err == util.ErrQueueClosed {
				err = kendynet.ErrSocketClose
			} else if err == util.ErrQueueFull {
				err = kendynet.ErrSendQueFull
			}
			return err
		}
	}
	return nil
}

func (this *StreamSocket) recvThreadFunc() {

	for !this.isClosed() {

		var p interface{}
		var err error

		recvTimeout := this.recvTimeout

		if recvTimeout > 0 {
			this.conn.SetReadDeadline(time.Now().Add(recvTimeout))
			p, err = this.receiver.ReceiveAndUnpack(this)
			this.conn.SetReadDeadline(time.Time{})
		} else {
			p, err = this.receiver.ReceiveAndUnpack(this)
		}

		if this.isClosed() {
			//上层已经调用关闭，所有事件都不再传递上去
			break
		}
		if err != nil || p != nil {
			var event kendynet.Event
			event.Session = this
			if err != nil {
				event.EventType = kendynet.EventTypeError
				event.Data = err
				this.mutex.Lock()
				if err == io.EOF {
					this.flag |= rclosed
				} else if !kendynet.IsNetTimeout(err) {
					kendynet.Errorf("ReceiveAndUnpack error:%s\n", err.Error())
					this.flag |= (rclosed | wclosed)
				}
				this.mutex.Unlock()
			} else {
				event.EventType = kendynet.EventTypeMessage
				event.Data = p
			}
			/*出现错误不主动退出循环，除非用户调用了session.Close()
			 * 避免用户遗漏调用Close(不调用Close会持续通告错误)
			 */
			this.onEvent(&event)
			if this.isClosed() {
				break
			}
		}
	}
}

func (this *StreamSocket) sendThreadFunc() {

	var err error

	defer func() {
		this.sendCloseChan <- 1
	}()

	writer := bufio.NewWriterSize(this.conn, 65535*2)

	for {
		closed, localList := this.sendQue.Get()
		size := len(localList)
		if closed && size == 0 {
			break
		}

		for i := 0; i < size; i++ {
			msg := localList[i].(kendynet.Message)

			data := msg.Bytes()
			for data != nil || (i == (size-1) && writer.Buffered() > 0) {
				if data != nil {
					var s int
					if len(data) > writer.Available() {
						s = writer.Available()
					} else {
						s = len(data)
					}
					writer.Write(data[:s])

					if s != len(data) {
						data = data[s:]
						//kendynet.Errorln("s != len(data)")
					} else {
						data = nil
					}
				}

				if writer.Available() == 0 || i == (size-1) {

					timeout := this.sendTimeout
					if timeout > 0 {
						this.conn.SetWriteDeadline(time.Now().Add(timeout))
						err = writer.Flush()
						this.conn.SetWriteDeadline(time.Time{})
					} else {
						err = writer.Flush()
					}
					if err != nil && err != io.ErrShortWrite {
						if this.sendQue.Closed() {
							return
						}
						if kendynet.IsNetTimeout(err) {
							err = kendynet.ErrSendTimeout
						} else {
							kendynet.Errorf("writer.Flush error:%s\n", err.Error())
							this.mutex.Lock()
							this.flag |= wclosed
							this.mutex.Unlock()
						}
						event := &kendynet.Event{Session: this, EventType: kendynet.EventTypeError, Data: err}
						this.onEvent(event)
						if this.sendQue.Closed() {
							return
						}
					}
				}
			}
		}
	}
}

func NewStreamSocket(conn net.Conn, sendQueueSize ...int) kendynet.StreamSession {
	if nil == conn {
		return nil
	} else {
		switch conn.(type) {
		case *net.TCPConn:
			break
		case *net.UnixConn:
			break
		default:
			kendynet.Errorf("NewStreamSocket() invaild conn type\n")
			return nil
		}

		s := &StreamSocket{
			conn:       conn,
			SocketBase: &SocketBase{},
		}
		s.sendQue = util.NewBlockQueue(sendQueueSize...)
		s.sendCloseChan = make(chan int, 1)
		s.imp = s
		return s
	}

	return nil
}

func (this *StreamSocket) Read(b []byte) (int, error) {
	return this.conn.Read(b)
}

func (this *StreamSocket) getSocketConn() net.Conn {
	return this.conn
}

func (this *StreamSocket) GetUnderConn() interface{} {
	return this.conn
}
