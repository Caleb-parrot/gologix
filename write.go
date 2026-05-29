package gologix

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
)

// WriteMulti writes multiple tags efficiently in a single request using a struct with gologix field tags.
func (client *Client) WriteMulti(value any) error {
	err := client.checkConnection()
	if err != nil {
		return fmt.Errorf("could not start multi write: %w", err)
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Struct {
		d, err := multi_to_dict(value)
		if err != nil {
			return fmt.Errorf("problem creating keyvalue dict %w", err)
		}
		return client.WriteMap(d)
	}
	return fmt.Errorf("value must be a struct with gologix tags")
}

// Write writes a single value to a single tag in the PLC.
func (client *Client) Write(tag string, value any) error {
	err := client.checkConnection()
	if err != nil {
		return fmt.Errorf("could not start write: %w", err)
	}

	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Struct {
		return client.write_udt(tag, value)
	}
	return client.write_single(tag, value)
}

// write_single handles scalars and arrays/slices (Fixed for full array support)
func (client *Client) write_single(tag string, value any) error {
	datatype, err := GoVarToCIPType(value)
	if err != nil {
		return fmt.Errorf("unsupported type for tag %s: %w", tag, err)
	}

	ioi, err := client.newIOI(tag, datatype)
	if err != nil {
		return fmt.Errorf("problem generating IOI for %s: %w", tag, err)
	}

	// Handle array/slice length
	elements := uint16(1)
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		if v.Len() == 0 {
			return fmt.Errorf("cannot write empty slice/array to %s", tag)
		}
		elements = uint16(v.Len())
	}

	ioi_header := msgCIPIOIHeader{
		Sequence: uint16(sequencer()),
		Service:  CIPService_Write,
		Size:     byte(len(ioi.Buffer) / 2),
	}

	ioi_footer := msgCIPWriteIOIFooter{
		DataType: uint16(datatype),
		Elements: elements,
	}

	reqitems := make([]CIPItem, 2)
	reqitems[0] = newItem(cipItem_ConnectionAddress, &client.OTNetworkConnectionID)
	reqitems[1] = CIPItem{Header: cipItemHeader{ID: cipItem_ConnectedData}}

	err = reqitems[1].Serialize(ioi_header)
	if err != nil {
		return fmt.Errorf("problem serializing ioi header: %w", err)
	}
	err = reqitems[1].Serialize(ioi.Buffer)
	if err != nil {
		return fmt.Errorf("problem serializing ioi: %w", err)
	}
	err = reqitems[1].Serialize(ioi_footer)
	if err != nil {
		return fmt.Errorf("problem serializing write footer: %w", err)
	}

	// Serialize the actual data
	dataBuf := bytes.NewBuffer([]byte{})
	err = binary.Write(dataBuf, binary.LittleEndian, value)
	if err != nil {
		return fmt.Errorf("problem encoding value for write: %w", err)
	}

	err = reqitems[1].Serialize(dataBuf.Bytes())
	if err != nil {
		return fmt.Errorf("problem serializing data: %w", err)
	}

	itemdata, err := serializeItems(reqitems)
	if err != nil {
		return err
	}

	hdr, data, err := client.send_recv_data(cipCommandSendUnitData, itemdata)
	if err != nil {
		return fmt.Errorf("failed to send write request: %w", err)
	}
	if hdr.Status != 0 {
		return fmt.Errorf("got non-success status %d when writing", hdr.Status)
	}

	// Parse response
	read_result_header := msgCIPResultHeader{}
	err = binary.Read(data, binary.LittleEndian, &read_result_header)
	if err != nil {
		client.Logger.Warn("Problem reading read result header", "error", err)
	}

	items, err := readItems(data)
	if err != nil {
		return fmt.Errorf("problem reading items from write, %w", err)
	}

	var hdr2 msgWriteResultHeader
	err = items[1].DeSerialize(&hdr2)
	if err != nil {
		return fmt.Errorf("problem deserializing write response header, %w", err)
	}
	if hdr2.Status != CIPStatus_OK {
		extended := uint16(0)
		if hdr2.StatusExtended == 1 {
			err = items[1].DeSerialize(&extended)
			return fmt.Errorf("got status %d:%d:%d instead of 0 in write response", hdr2.Status, hdr2.StatusExtended, extended)
		}
		return fmt.Errorf("got status %d instead of 0 in write response", hdr2.Status)
	}

	return nil
}

// write_udt remains unchanged from original
func (client *Client) write_udt(tag string, value any) error {
	datatype := CIPTypeStruct
	ioi, err := client.newIOI(tag, datatype)
	if err != nil {
		return fmt.Errorf("problem generating IOI. %w", err)
	}
	elements := uint16(1)

	ioi_header := msgCIPIOIHeader{
		Sequence: uint16(sequencer()),
		Service:  CIPService_Write,
		Size:     byte(len(ioi.Buffer) / 2),
	}

	_, typecrc, err := TypeEncode(value)
	if err != nil {
		return fmt.Errorf("problem encoding type. %w", err)
	}

	UDTdata := bytes.NewBuffer([]byte{})
	_, err = Pack(UDTdata, value)
	if err != nil {
		return fmt.Errorf("problem packing data. %w", err)
	}

	reqitems := make([]CIPItem, 2)
	reqitems[0] = newItem(cipItem_ConnectionAddress, &client.OTNetworkConnectionID)
	reqitems[1] = CIPItem{Header: cipItemHeader{ID: cipItem_ConnectedData}}

	err = reqitems[1].Serialize(ioi_header)
	if err != nil {
		return fmt.Errorf("problem serializing ioi header. %w", err)
	}
	err = reqitems[1].Serialize(ioi.Buffer)
	if err != nil {
		return fmt.Errorf("problem serializing ioi buffer. %w", err)
	}
	err = reqitems[1].Serialize(datatype)
	if err != nil {
		return fmt.Errorf("problem serializing datatype. %w", err)
	}

	err = reqitems[1].Serialize(byte(2))
	if err != nil {
		return fmt.Errorf("problem serializing number of elements. %w", err)
	}

	err = reqitems[1].Serialize(typecrc)
	if err != nil {
		return fmt.Errorf("problem serializing type crc. %w", err)
	}

	err = reqitems[1].Serialize(elements)
	if err != nil {
		return fmt.Errorf("problem serializing elements. %w", err)
	}

	err = reqitems[1].Serialize(UDTdata)
	if err != nil {
		return fmt.Errorf("problem serializing UDT data. %w", err)
	}

	itemdata, err := serializeItems(reqitems)
	if err != nil {
		return err
	}
	hdr, data, err := client.send_recv_data(cipCommandSendUnitData, itemdata)
	if err != nil {
		return err
	}
	if hdr.Status != 0 {
		return fmt.Errorf("got non-success status %d when writing", hdr.Status)
	}
	read_result_header := msgCIPResultHeader{}
	err = binary.Read(data, binary.LittleEndian, &read_result_header)
	if err != nil {
		client.Logger.Warn("Problem reading read result header", "error", err)
	}
	items, err := readItems(data)
	if err != nil {
		return fmt.Errorf("problem reading items from write, %w", err)
	}
	var hdr2 msgWriteResultHeader
	err = items[1].DeSerialize(&hdr2)
	if err != nil {
		return fmt.Errorf("problem deserializing write response header, %w", err)
	}
	if hdr2.Status != CIPStatus_OK {
		extended := uint16(0)
		if hdr2.StatusExtended == 1 {
			err = items[1].DeSerialize(&extended)
			return fmt.Errorf("got status %d:%d:%d instead of 0 in write response. Problem getting extended status %w",
				hdr2.Status, hdr2.StatusExtended, extended, err)
		}
		return fmt.Errorf("got status %d instead of 0 in write response", hdr2.Status)
	}

	return nil
}
