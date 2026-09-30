<?php
// relations() の書き方のいろいろ(#49)
class Form extends AppActiveRecord
{
    public function tableName() { return 'forms'; }
    public function relations()
    {
        return array(
            // FK を列の対応で書く(BELONGS_TO は自分側の列)
            'owner' => array(self::BELONGS_TO, 'Contact', array('owner_id' => 'id')),
            // HAS_ONE は相手側の列
            'setting' => array(self::HAS_ONE, 'Setting', array('form_id' => 'id')),
            // FK を空にして on 句で結ぶ(列が読める)
            'answers' => array(self::HAS_MANY, 'Answer', '', 'on' => "$alias.id = answers.form_ref"),
            // on 句から列を特定できない
            'latest' => array(self::HAS_ONE, 'Answer', '', 'on' => 'created_at > NOW()'),
            // 相手を ::class で書く
            'account' => array(self::BELONGS_TO, Account::class, 'account_id'),
            // クラス名の大文字小文字違い(PHP は区別しない)
            'reviewer' => array(self::BELONGS_TO, 'contact', 'reviewer_id'),
            // コメントアウトした宣言は読まない
            // 'ghost' => array(self::BELONGS_TO, 'Account', 'ghost_id'),
            /* 'ghost2' => array(self::BELONGS_TO, 'Account', 'ghost2_id'), */
        );
    }
}
