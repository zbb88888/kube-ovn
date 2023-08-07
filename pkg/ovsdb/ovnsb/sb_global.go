// Code generated by "libovsdb.modelgen"
// DO NOT EDIT.

package ovnsb

const SBGlobalTable = "SB_Global"

// SBGlobal defines an object in SB_Global table
type SBGlobal struct {
	UUID        string            `ovsdb:"_uuid"`
	Connections []string          `ovsdb:"connections"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Ipsec       bool              `ovsdb:"ipsec"`
	NbCfg       int               `ovsdb:"nb_cfg"`
	Options     map[string]string `ovsdb:"options"`
	SSL         *string           `ovsdb:"ssl"`
}
