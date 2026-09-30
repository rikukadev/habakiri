<?php
// 変数に組み立てて返す relations()。ループが無いので書かれた宣言がすべて
class Event extends AppActiveRecord
{
    public function tableName() { return 'events'; }
    public function relations()
    {
        $r = array();
        $r = array_merge($r, array(
            'owner' => array(self::BELONGS_TO, 'Contact', 'owner_id'),
        ));
        $r['account'] = array(self::BELONGS_TO, 'Account', 'account_id');
        // STAT(件数の集計)も相手側の FK 列を言う
        $r['answerCount'] = array(self::STAT, 'Answer', 'event_id');
        return $r;
    }
}
