package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const (
	PACKET_OVERHEAD    = 12
	DATA_PACKET_SIZE   = 2096
	STREAM_PACKET_SIZE = DATA_PACKET_SIZE - PACKET_OVERHEAD
)

type StreamConn struct {
	LastRemotePacAdd      net.Addr
	ForwardConnection     net.Conn
	StreamID              uint64
	PacketID              uint64
	LastForwardedPacketID uint64
	pc                    net.PacketConn
	PacketList            map[uint64][]byte
	OutgoingPacketList    map[uint64][]byte
	IsClosed              bool
}

func (sc *StreamConn) Write(b []byte) (n int, err error) {
	n, err = sc.pc.WriteTo(b, sc.LastRemotePacAdd)
	return
}
func (sc *StreamConn) Close() {
	sc.IsClosed = true
}

type DataStream struct {
	ConfStream *StreamConn
}

func (Ds *DataStream) Write(b []byte) (n int, err error) {

	DatPack := &StreamPacket{Command: 2, StreamID: Ds.ConfStream.StreamID, PacketID: Ds.ConfStream.PacketID, Data: b}
	Ds.ConfStream.PacketID++
	DatBytes := DatPack.Marchal()
	Ds.ConfStream.OutgoingPacketList[DatPack.PacketID] = DatBytes

	n = len(b)
	fmt.Println("dataPacket -> ", n)
	_, err = Ds.ConfStream.Write(DatBytes)
	return
}

type StreamPacket struct {
	Command  uint64
	StreamID uint64
	PacketID uint64
	Data     []byte
}

func (Stp *StreamPacket) Marchal() []byte {
	SubmitData := make([]byte, PACKET_OVERHEAD)
	PackPos := 0
	PackPos += binary.PutUvarint(SubmitData, Stp.Command)
	PackPos += binary.PutUvarint(SubmitData[PackPos:], Stp.PacketID)

	PackPos += binary.PutUvarint(SubmitData[PackPos:], Stp.StreamID)

	return append(SubmitData[:PackPos], Stp.Data...)
}

func UnmarchalStreamPacket(data []byte) (Stp *StreamPacket, err error) {
	Stp = &StreamPacket{}
	Command, ReadBytes := binary.Uvarint(data)
	if ReadBytes <= 0 {
		return Stp, errors.New("Malfomred Command")
	}
	Stp.Command = Command
	data = data[ReadBytes:]

	PacketID, ReadBytes := binary.Uvarint(data)
	if ReadBytes <= 0 {
		return Stp, errors.New("Malfomred PacketID")
	}
	Stp.PacketID = PacketID
	data = data[ReadBytes:]

	StreamId, ReadBytes := binary.Uvarint(data)
	if ReadBytes <= 0 {
		return Stp, nil
	}
	Stp.StreamID = StreamId
	data = data[ReadBytes:]

	Stp.Data = data
	return
}

func RunServer() {
	var err error
	//Listen for new conenctions from clients
	ServerUdpListener, err = net.ListenPacket("udp", ":5453")
	if err != nil {
		fmt.Println("Error creating packet listener:", err.Error())
		return
	}
	// Close the listener when the application closes.
	defer ServerUdpListener.Close()

	//Connection identifier
	var ConnectionID uint64 = 50

	StreamMap := map[uint64]*StreamConn{}

	fmt.Println("Server Listening on :5453")
	for {
		// Listen for an incoming Packet
		recivedData := make([]byte, DATA_PACKET_SIZE) //Big enogh to get an full mtu
		DataAmount, RecivedFrom, err := ServerUdpListener.ReadFrom(recivedData)
		if err != nil {
			fmt.Println("Error Reciving packet: ", err.Error())
			return
		}
		UsableData := recivedData[:DataAmount]
		//fmt.Println("Recived packet from:", RecivedFrom, "data amount:", DataAmount)

		RecivedPacket, err := UnmarchalStreamPacket(UsableData)
		if err != nil {
			fmt.Println("Error Decoding packet: ", err.Error())
			return
		}

		if RecivedPacket.Command == 10 {

			fmt.Println("Client Requesting new Stream Sending them stream id:", ConnectionID)

			NewStream := &StreamConn{LastRemotePacAdd: RecivedFrom, StreamID: ConnectionID, pc: ServerUdpListener, PacketList: make(map[uint64][]byte), OutgoingPacketList: make(map[uint64][]byte), IsClosed: false}
			ServiceAddr := "localhost:1337"

			//COnnect to service
			NewStream.ForwardConnection, err = net.Dial("tcp", ServiceAddr)
			if err != nil {
				fmt.Println("Error connecting to Service:", err.Error())
				NewStream.Close()
				continue
			}
			defer NewStream.ForwardConnection.Close()

			ticker := time.NewTicker(time.Second * 1)
			go func() {
				defer ticker.Stop()
				for _ = range ticker.C {
					if ControllStream(NewStream) {
						return
					}
				}
			}()

			StreamMap[ConnectionID] = NewStream

			ResponsePack := &StreamPacket{Command: 10, StreamID: ConnectionID}
			NewStream.Write(ResponsePack.Marchal())

			ConnectionID++
			continue
		}

		if Stream, ok := StreamMap[RecivedPacket.StreamID]; ok {
			if RecivedPacket.Command == 11 { //Client Says ok lets go
				DatWriter := &DataStream{ConfStream: Stream}
				go Pipe(Stream.ForwardConnection, DatWriter, Stream)
			}
			if RecivedPacket.Command == 2 {
				HandlePacketData(Stream, RecivedPacket, "server")
			}
		} else {
			fmt.Println("Unknown Stream id", RecivedPacket.StreamID)
		}
	}
}

func ControllStream(Stream *StreamConn) bool {
	fmt.Println("Controlling stream width id:", Stream.StreamID)
	var MinKey uint64 = 9999999999
	var MaxKey uint64 = 0
	for key, _ := range Stream.PacketList {
		if MinKey > key {
			MinKey = key
		}
		if MaxKey < key {
			MaxKey = key
		}
	}
	var i uint64
	for i = MinKey; i <= MaxKey; i++ {
		if _, ok := Stream.PacketList[i]; ok {
			fmt.Println("Missing packetID:", i)

		}
	}
	return Stream.IsClosed
}

func RunClient() {
	//Listen for conenctions from the ssh client
	l, err := net.Listen("tcp", ":3333")
	if err != nil {
		fmt.Println("Error listening for incoming con:", err.Error())
		os.Exit(1)
	}
	// Close the listener when the application closes.
	defer l.Close()

	fmt.Println("Client Listening on :3333")
	for {
		// Listen for an incoming connection.
		conn, err := l.Accept()
		if err != nil {
			fmt.Println("Error accepting new ssh client: ", err.Error())
			return
		}

		go handleOutgoingTunnel(conn)
	}
}

func HandlePacketData(Stream *StreamConn, RecivedPacket *StreamPacket, ErrStr string) {
	var err error
	if _, ok := Stream.PacketList[RecivedPacket.PacketID]; ok {
		fmt.Println(RecivedPacket.PacketID, " Has allredy been recived")
	} else {
		Stream.PacketList[RecivedPacket.PacketID] = RecivedPacket.Data
	}
	var MinKey uint64 = 9999999999
	var MaxKey uint64 = 0
	for key, _ := range Stream.PacketList {
		if MinKey > key {
			MinKey = key
		}
		if MaxKey < key {
			MaxKey = key
		}
	}

	var i uint64
	for i = MinKey; i <= MaxKey; i++ {
		if Stream.LastForwardedPacketID+1 == i || i == 0 {
			if packetData, ok := Stream.PacketList[i]; ok {
				fmt.Println(RecivedPacket.PacketID, "to "+ErrStr+" ssh ->", len(RecivedPacket.Data))
				_, err = Stream.ForwardConnection.Write(packetData)
				if err != nil {
					fmt.Println("error Writing packet to ssh "+ErrStr+": ", err.Error())
					Stream.Close()
				}

				Stream.LastForwardedPacketID = i
				delete(Stream.PacketList, i)
			} else {
				fmt.Println("error missing packet:", i)
			}
		}
	}
}

var ServerUdpListener net.PacketConn

func main() {
	IsServer := flag.Bool("server", false, "If this is the server instance")
	flag.Parse()

	if *IsServer {
		RunServer()
	} else {
		RunClient()
	}
}

type Tunnel struct {
	ID         int
	ClientConn net.Conn
	ServerConn net.Conn
}

// Handles incoming connection to client
func handleOutgoingTunnel(clinetConn net.Conn) {
	defer clinetConn.Close()
	fmt.Println("Got connection from ssh start tunneling it to the server")
	ServerConn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		fmt.Println("Error Dailing Server:", err.Error())
		return
	}
	defer ServerConn.Close()

	Stream := &StreamConn{pc: ServerConn, PacketList: make(map[uint64][]byte), OutgoingPacketList: make(map[uint64][]byte), IsClosed: false}
	Stream.LastRemotePacAdd, err = net.ResolveUDPAddr("udp", "localhost:5453")
	Stream.ForwardConnection = clinetConn

	ticker := time.NewTicker(time.Second * 1)
	go func() {
		defer ticker.Stop()
		for _ = range ticker.C {
			if ControllStream(Stream) {
				return
			}
		}
	}()

	//Request New Stream
	RequestPack := &StreamPacket{Command: 10}
	Stream.Write(RequestPack.Marchal())

	for {
		// Listen for an incoming Packet
		recivedData := make([]byte, DATA_PACKET_SIZE)
		DataAmount, _, err := ServerConn.ReadFrom(recivedData)
		if err != nil {
			fmt.Println("Error Reciving packet: ", err.Error())
			return
		}
		UsableData := recivedData[:DataAmount]

		RecivedPacket, err := UnmarchalStreamPacket(UsableData)
		if err != nil {
			fmt.Println("Error Decoding packet: ", err.Error())
			return
		}

		if RecivedPacket.Command == 10 {
			Stream.StreamID = RecivedPacket.StreamID
			ReqPack := &StreamPacket{Command: 11, StreamID: Stream.StreamID}
			Stream.Write(ReqPack.Marchal())

			DatWriter := &DataStream{ConfStream: Stream}
			go Pipe(Stream.ForwardConnection, DatWriter, Stream)
		}
		if RecivedPacket.Command == 2 {
			HandlePacketData(Stream, RecivedPacket, "client")
		}
	}
}

func Pipe(from io.Reader, to io.Writer, Stream *StreamConn) {
	recivedData := make([]byte, STREAM_PACKET_SIZE) //Big enogh to get an full mtu
	for {
		amountOfData, err := from.Read(recivedData)
		if err != nil {
			fmt.Println("Error reading data:", err.Error())
			Stream.Close()
			return
		}

		amountOfSubmitedData, err := to.Write(recivedData[:amountOfData])
		if err != nil {
			fmt.Println("Error writing data:", err.Error())
			Stream.Close()
			return
		}
		if amountOfSubmitedData != amountOfData {
			fmt.Println("Error could not write the data we recived", amountOfSubmitedData, amountOfData)
			//return
		}
	}
}
